package checks

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"strings"

	"github.com/jonog/redalert/utils"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func init() {
	Register("remote-docker", NewDockerRemoteDocker)
}

type RemoteDocker struct {
	User string
	Host string
	Tool string
	log  *log.Logger
}

var NewDockerRemoteDocker = func(config Config, logger *log.Logger) (Checker, error) {

	tool := utils.StringDefault(config.Tool, "nc")
	if !utils.FindStringInArray(tool, []string{"nc", "socat"}) {
		return nil, errors.New("checks: unknown tool in remote docker config")
	}

	return Checker(&RemoteDocker{
		User: config.User,
		Host: config.Host,
		Tool: tool,
		log:  logger,
	}), nil
}

func runCommand(client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", nil
	}
	defer session.Close()
	var b bytes.Buffer
	session.Stdout = &b
	err = session.Run(cmd)
	if err != nil {
		return "", nil
	}
	output := b.String()
	return output, nil
}

func parseAndUnmarshal(raw string, data interface{}) error {

	httpRawSplit := strings.Split(raw, "\n\r\n")
	if len(httpRawSplit) < 2 {
		return errors.New("invalid format")
	}

	jsonStr := httpRawSplit[1]
	return json.Unmarshal([]byte(jsonStr), data)
}

func (r *RemoteDocker) dockerAPISocketAccess() string {
	if r.Tool == "nc" {
		return "nc -U /var/run/docker.sock"
	}
	if r.Tool == "socat" {
		return "socat - UNIX-CONNECT:/var/run/docker.sock"
	}
	return ""
}

func (r *RemoteDocker) dockerAPIStreamSocketAccess() string {
	if r.Tool == "nc" {
		return "nc -U /var/run/docker.sock"
	}
	if r.Tool == "socat" {
		return "socat -t 2 - UNIX-CONNECT:/var/run/docker.sock"
	}
	return ""
}

func (r *RemoteDocker) Check() (Metrics, error) {

	output := Metrics(make(map[string]*float64))

	// TODO:
	// add SSH auth options involving password / key

	sshAgent, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		return output, nil
	}
	auth := ssh.PublicKeysCallback(agent.NewClient(sshAgent).Signers)
	defer sshAgent.Close()

	client, err := ssh.Dial("tcp", r.Host+":"+"22", &ssh.ClientConfig{
		User: r.User,
		Auth: []ssh.AuthMethod{auth},
	})
	if err != nil {
		return output, nil
	}

	sshOutput, err := runCommand(client, `echo -e "GET /containers/json HTTP/1.0\r\n" | `+r.dockerAPISocketAccess())
	if err != nil {
		return output, nil
	}

	if len(sshOutput) == 0 {
		r.log.Println("ERROR: cannot get list of containers from docker remote API")
		return output, nil
	}

	var containers []Container
	err = parseAndUnmarshal(sshOutput, &containers)
	if err != nil {
		return output, nil

	}

	for _, c := range containers {

		cmd := `(timeout 3 <<<'GET /containers/` + c.Id + `/stats HTTP/1.0'$'\r'$'\n' ` + r.dockerAPIStreamSocketAccess() + ` | cat) | tail -2`

		sshOutput, err := runCommand(client, cmd)
		if err != nil {
			r.log.Println("ERROR: unable to successfully ssh to obtain container stats", err)
			continue
		}

		if len(sshOutput) == 0 {
			r.log.Println("ERROR: cannot get container stats from docker remote API")
			continue
		}

		readings := strings.Split(sshOutput, "\n")
		if len(readings) < 2 {
			r.log.Println("ERROR: two readings were not obtained from docker remote API")
			continue
		}

		var containerStats1 ContainerStats
		err = json.Unmarshal([]byte(readings[0]), &containerStats1)
		if err != nil {
			r.log.Println("ERROR: unmarshalling container stats json (1st reading)", err)
			continue
		}
		var containerStats2 ContainerStats
		err = json.Unmarshal([]byte(readings[1]), &containerStats2)
		if err != nil {
			r.log.Println("ERROR: unmarshalling container stats json (2nd reading)", err)
			continue
		}

		containerName, err := getContainerName(c.Names)
		if err != nil {
			r.log.Println("ERROR: establishing container name", err)
			continue
		}

		// TODO: collect all the metrics
		containerMemory := float64(containerStats2.MemoryStats.Usage / 1000000.0)
		output[containerName+".memory"] = &containerMemory

		cpuUsageDelta := float64(containerStats2.CpuStats.CpuUsage.TotalUsage) - float64(containerStats1.CpuStats.CpuUsage.TotalUsage)
		systemCpuUsageDelta := float64(containerStats2.CpuStats.SystemCpuUsage) - float64(containerStats1.CpuStats.SystemCpuUsage)
		cpuUsagePercent := cpuUsageDelta * 100 / systemCpuUsageDelta

		output[containerName+".cpu"] = &cpuUsagePercent

	}

	containerCount := float64(len(containers))
	output["container_count"] = &containerCount

	return output, nil
}

func getContainerName(names []string) (string, error) {

	// remove prefix '/'
	for _, name := range names {
		namePrefixRemoved := name[1:]

		// find container without '/' within name
		if len(strings.Split(namePrefixRemoved, "/")) == 1 {
			return namePrefixRemoved, nil
		}
	}

	return "", errors.New("remote-docker: unable to find container name")
}

func (r *RemoteDocker) MetricInfo(metric string) MetricInfo {
	return MetricInfo{Unit: ""}
}

func (r *RemoteDocker) MessageContext() string {
	return "docker host - " + r.Host
}

type Container struct {
	Command string
	Created int
	Id      string
	Image   string
	Names   []string
	Ports   []PortConfig
	Status  string
}

type PortConfig struct {
	IP          string
	PrivatePort int
	PublicPort  int
	Type        string
}

type ContainerStats struct {
	Read    string `json:"read"`
	Network struct {
		RxDropped int `json:"rx_dropped"`
		RxBytes   int `json:"rx_bytes"`
		RxErrors  int `json:"rx_errors"`
		TxPackets int `json:"tx_packets"`
		TxDropped int `json:"tx_dropped"`
		RxPackets int `json:"rx_packets"`
		TxErrors  int `json:"tx_errors"`
		TxBytes   int `json:"tx_bytes"`
	} `json:"network"`
	MemoryStats struct {
		Stats struct {
			TotalRss int `json:"total_rss"`
			// TODO: add additional mem stats
		} `json:"stats"`
		MaxUsage int `json:"max_usage"`
		Usage    int `json:"usage"`
		Failcnt  int `json:"failcnt"`
		Limit    int `json:"limit"`
	} `json:"memory_stats"`
	CpuStats struct {
		CpuUsage struct {
			PercpuUsage       []int `json:"percpu_usage"`
			UsageInUsermode   int   `json:"usage_in_usermode"`
			TotalUsage        int   `json:"total_usage"`
			UsageInKernelmode int   `json:"usage_in_kernelmode"`
		} `json:"cpu_usage"`
		SystemCpuUsage int `json:"system_cpu_usage"`
	} `json:"cpu_stats"`
}
