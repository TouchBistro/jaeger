package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/TouchBistro/goutils/fatal"
	"github.com/TouchBistro/jaeger/aws"
	"github.com/spf13/cobra"
)

type sshOptions struct {
	clusterName      string
	sshPublicKeyPath string
}

var sshOpts sshOptions

var sshCmd = &cobra.Command{
	Use:   "ssh <service>",
	Args:  cobra.ExactArgs(1),
	Short: "ssh directly into an ECS container.",
	Run: func(cmd *cobra.Command, args []string) {
		serviceName := args[0]
		container, err := aws.FindServiceContainer(cmd.Context(), aws.FindServiceContainerOptions{
			ServiceName:      serviceName,
			ClusterName:      sshOpts.clusterName,
			SSHPublicKeyPath: sshOpts.sshPublicKeyPath,
		})
		if err != nil {
			fatal.ExitErrf(err, "failed to find container for %s", serviceName)
		}

		// Exec into ssh, passing the container id to drop right into shell
		dockerCmd := fmt.Sprintf("docker exec -it %s bash", container.ContainerID)
		execCmd := exec.Command("ssh", "-t", "ec2-user@"+container.InstanceDNSName, dockerCmd)
		execCmd.Stdin = os.Stdin
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr

		err = execCmd.Run()
		if err != nil {
			fatal.ExitErr(err, "failed to ssh into container")
		}
	},
}

func init() {
	sshCmd.Flags().StringVar(&sshOpts.clusterName, "cluster", "", "The ECS cluster the service is in")
	sshCmd.Flags().StringVar(&sshOpts.sshPublicKeyPath, "ssh-key-path", "", "The path to the ssh public key file")
	rootCmd.AddCommand(sshCmd)
}
