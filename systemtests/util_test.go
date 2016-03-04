package systemtests

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	utils "github.com/contiv/systemtests-utils"
	"github.com/contiv/vagrantssh"
	"github.com/contiv/volplugin/config"
)

func (s *systemtestSuite) mon0cmd(command string) (string, error) {
	return s.vagrant.GetNode("mon0").RunCommandWithOutput(command)
}

func (s *systemtestSuite) docker(command string) (string, error) {
	return s.mon0cmd("docker " + command)
}

func (s *systemtestSuite) volcli(command string) (string, error) {
	return s.mon0cmd("volcli " + command)
}

func (s *systemtestSuite) readIntent(fn string) (*config.PolicyConfig, error) {
	content, err := ioutil.ReadFile(fn)
	if err != nil {
		return nil, err
	}

	cfg := &config.PolicyConfig{}

	if err := json.Unmarshal(content, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (s *systemtestSuite) purgeVolume(host, policy, name string, purgeCeph bool) error {
	log.Infof("Purging %s/%s. Purging ceph: %v", host, name, purgeCeph)

	// ignore the error here so we get to the purge if we have to
	s.vagrant.GetNode(host).RunCommand(fmt.Sprintf("docker volume rm %s/%s", policy, name))

	defer func() {
		if purgeCeph {
			s.vagrant.GetNode("mon0").RunCommand(fmt.Sprintf("sudo rbd rm rbd/%s.%s", policy, name))
		}
	}()

	if out, err := s.volcli(fmt.Sprintf("volume remove %s/%s", policy, name)); err != nil {
		log.Error(out)
		return err
	}

	return nil
}

func (s *systemtestSuite) purgeVolumeHost(policy, host string, purgeCeph bool) {
	s.purgeVolume(host, policy, host, purgeCeph)
}

func (s *systemtestSuite) createVolumeHost(policy, host string, opts map[string]string) error {
	return s.createVolume(host, policy, host, opts)
}

func (s *systemtestSuite) createVolume(host, policy, name string, opts map[string]string) error {
	log.Infof("Creating %s/%s on %q", policy, name, host)

	optsStr := []string{}

	if opts != nil {
		for key, value := range opts {
			optsStr = append(optsStr, "--opt")
			optsStr = append(optsStr, key+"="+value)
		}
	}

	cmd := fmt.Sprintf("docker volume create -d volplugin --name %s/%s %s", policy, name, strings.Join(optsStr, " "))

	if out, err := s.vagrant.GetNode(host).RunCommandWithOutput(cmd); err != nil {
		log.Info(string(out))
		return err
	}

	if out, err := s.volcli(fmt.Sprintf("volume get %s/%s", policy, name)); err != nil {
		log.Error(out)
		return err
	}

	return nil
}

func (s *systemtestSuite) uploadGlobal(configFile string) error {
	log.Infof("Uploading global configuration %s", configFile)
	out, err := s.volcli(fmt.Sprintf("global upload < /testdata/%s.json", configFile))
	if err != nil {
		log.Println(out)
	}

	return err
}

func (s *systemtestSuite) rebootstrap() error {
	s.clearContainers()
	stopVolsupervisor(s.vagrant.GetNode("mon0"))
	s.vagrant.IterateNodes(stopVolplugin)
	s.vagrant.IterateNodes(stopVolmaster)
	s.clearRBD()

	utils.ClearEtcd(s.vagrant.GetNode("mon0"))

	if err := s.restartDocker(); err != nil {
		return err
	}

	if err := s.uploadGlobal("global1"); err != nil {
		return err
	}

	if err := s.vagrant.IterateNodes(startVolmaster); err != nil {
		return err
	}

	if err := s.vagrant.IterateNodes(waitForVolmaster); err != nil {
		return err
	}

	if err := startVolsupervisor(s.vagrant.GetNode("mon0")); err != nil {
		return err
	}

	if err := waitForVolsupervisor(s.vagrant.GetNode("mon0")); err != nil {
		return err
	}

	if err := s.vagrant.IterateNodes(startVolplugin); err != nil {
		return err
	}

	if err := s.vagrant.IterateNodes(waitForVolplugin); err != nil {
		return err
	}

	if _, err := s.uploadIntent("policy1", "intent1"); err != nil {
		return err
	}

	return nil
}

func (s *systemtestSuite) uploadIntent(policyName, fileName string) (string, error) {
	log.Infof("Uploading intent %q as policy %q", fileName, policyName)
	return s.volcli(fmt.Sprintf("policy upload %s < /testdata/%s.json", policyName, fileName))
}

func runCommandUntilNoError(node vagrantssh.TestbedNode, cmd string, timeout int) error {
	runCmd := func() (string, bool) {
		if err := node.RunCommand(cmd); err != nil {
			return "", false
		}
		return "", true
	}
	timeoutMessage := fmt.Sprintf("timeout reached trying to run %v on %q", cmd, node.GetName())
	_, err := utils.WaitForDone(runCmd, 10*time.Millisecond, 10*time.Second, timeoutMessage)
	return err
}

func waitForVolsupervisor(node vagrantssh.TestbedNode) error {
	log.Infof("Checking if volsupervisor is running on %q", node.GetName())
	err := runCommandUntilNoError(node, "pgrep -c volsupervisor", 10)
	if err == nil {
		log.Infof("Volsupervisor is running on %q", node.GetName())

	}
	return nil
}

func waitForVolmaster(node vagrantssh.TestbedNode) error {
	log.Infof("Checking if volmaster is running on %q", node.GetName())
	err := runCommandUntilNoError(node, "pgrep -c volmaster", 10)
	if err == nil {
		log.Infof("Volmaster is running on %q", node.GetName())

	}
	return nil
}

func waitForVolplugin(node vagrantssh.TestbedNode) error {
	log.Infof("Checking if volplugin is running on %q", node.GetName())
	err := runCommandUntilNoError(node, "pgrep -c volplugin", 10)
	if err == nil {
		log.Infof("Volplugin is running on %q", node.GetName())

	}
	return nil
}

func (s *systemtestSuite) pullDebian() error {
	log.Infof("Pulling alpine:latest on all boxes")
	return s.vagrant.SSHExecAllNodes("docker pull alpine")
}

func startVolsupervisor(node vagrantssh.TestbedNode) error {
	log.Infof("Starting the volsupervisor on %q", node.GetName())
	return node.RunCommandBackground("(sudo -E nohup `which volsupervisor` --debug </dev/null 2>&1 | sudo tee -a /tmp/volsupervisor.log) &")
}

func stopVolsupervisor(node vagrantssh.TestbedNode) error {
	log.Infof("Stopping the volsupervisor on %q", node.GetName())
	return node.RunCommand("sudo pkill volsupervisor")
}

func startVolmaster(node vagrantssh.TestbedNode) error {
	log.Infof("Starting the volmaster on %q", node.GetName())
	err := node.RunCommandBackground("(sudo -E nohup `which volmaster` </dev/null 2>&1 | sudo tee -a /tmp/volmaster.log) &")
	log.Infof("Waiting for volmaster startup on %q", node.GetName())
	time.Sleep(10 * time.Millisecond)
	return err
}

func stopVolmaster(node vagrantssh.TestbedNode) error {
	log.Infof("Stopping the volmaster on %q", node.GetName())
	return node.RunCommand("sudo pkill volmaster")
}

func startVolplugin(node vagrantssh.TestbedNode) error {
	log.Infof("Starting the volplugin on %q", node.GetName())
	defer time.Sleep(10 * time.Millisecond)

	// FIXME this is hardcoded because it's simpler. If we move to
	// multimaster or change the monitor subnet, we will have issues.
	return node.RunCommandBackground("(sudo -E `which volplugin` 2>&1 | sudo tee -a /tmp/volplugin.log) &")
}

func stopVolplugin(node vagrantssh.TestbedNode) error {
	log.Infof("Stopping the volplugin on %q", node.GetName())
	return node.RunCommand("sudo pkill volplugin")
}

func restartDockerHost(node vagrantssh.TestbedNode) error {
	log.Infof("Restarting docker on %q", node.GetName())
	// note that for all these restart tasks we error out quietly to avoid other
	// hosts being cleaned up
	node.RunCommand("sudo service docker restart")
	return nil
}

func (s *systemtestSuite) restartDocker() error {
	return s.vagrant.IterateNodes(restartDockerHost)
}

func (s *systemtestSuite) clearContainerHost(node vagrantssh.TestbedNode) error {
	log.Infof("Clearing containers on %q", node.GetName())
	node.RunCommand("docker ps -aq | xargs docker rm -f")
	return nil
}

func (s *systemtestSuite) clearContainers() error {
	return s.vagrant.IterateNodes(s.clearContainerHost)
}

func (s *systemtestSuite) clearVolumeHost(node vagrantssh.TestbedNode) error {
	log.Infof("Clearing volumes on %q", node.GetName())
	node.RunCommand("docker volume ls | tail -n +2 | awk '{ print $2 }' | xargs docker volume rm")
	return nil
}

func (s *systemtestSuite) clearVolumes() error {
	return s.vagrant.IterateNodes(s.clearVolumeHost)
}

func (s *systemtestSuite) clearRBD() error {
	log.Info("Clearing rbd images")

	s.vagrant.IterateNodes(func(node vagrantssh.TestbedNode) error {
		s.vagrant.GetNode(node.GetName()).RunCommandWithOutput("for img in $(sudo rbd showmapped | tail -n +2 | awk \"{ print \\$5 }\"); do sudo umount $img; sudo umount -f $img; done")
		return nil
	})

	s.vagrant.IterateNodes(func(node vagrantssh.TestbedNode) error {
		s.vagrant.GetNode(node.GetName()).RunCommandWithOutput("for img in $(sudo rbd showmapped | tail -n +2 | awk \"{ print \\$5 }\"); do sudo umount $img; sudo rbd unmap $img; done")
		return nil
	})

	out, err := s.vagrant.GetNode("mon0").RunCommandWithOutput("for img in $(sudo rbd ls); do sudo rbd snap purge $img; sudo rbd rm $img; done")
	if err != nil {
		log.Info(out)
	}

	return err
}
