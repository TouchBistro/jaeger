package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/TouchBistro/goutils/fatal"
	"github.com/TouchBistro/jaeger/aws"
	"github.com/spf13/cobra"
)

type logsOptions struct {
	clusterName      string
	sshPublicKeyPath string
}

var logsOpts sshOptions

var logsCmd = &cobra.Command{
	Use:   "logs <service>",
	Args:  cobra.ExactArgs(1),
	Short: "Retrieve logs from dead containers of an ECS service",
	Run: func(cmd *cobra.Command, args []string) {
		serviceName := args[0]
		container, err := aws.FindServiceContainer(aws.FindServiceContainerOptions{
			ServiceName:      serviceName,
			ClusterName:      logsOpts.clusterName,
			SSHPublicKeyPath: logsOpts.sshPublicKeyPath,
			FindLogs:         true,
		})
		if err != nil {
			fatal.ExitErrf(err, "failed to find container for %s", serviceName)
		}

		// Exec into ssh, passing the container id to drop right into shell
		dockerCmd := fmt.Sprintf("docker logs %s", container.ContainerID)
		execCmd := exec.Command("ssh", "ec2-user@"+container.InstanceDNSName, dockerCmd)
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr
		err = execCmd.Run()
		if err != nil {
			fatal.ExitErr(err, "failed to ssh into container")
		}
	},
}

func init() {
	logsCmd.Flags().StringVar(&logsOpts.clusterName, "cluster", "", "The ECS cluster the service is in")
	logsCmd.Flags().StringVar(&logsOpts.sshPublicKeyPath, "ssh-key-path", "", "The path to the ssh public key file")
	rootCmd.AddCommand(logsCmd)
}
