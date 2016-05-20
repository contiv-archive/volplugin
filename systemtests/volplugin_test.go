package systemtests

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/contiv/volplugin/config"

	. "gopkg.in/check.v1"

	log "github.com/Sirupsen/logrus"
)

func (s *systemtestSuite) TestVolpluginLockFreeOperation(c *C) {
	if !nfsDriver() {
		c.Skip("Cannot run this test on any driver but NFS")
		return
	}

	out, err := s.uploadIntent("policy1", "unlocked")
	c.Assert(err, IsNil, Commentf(out))
	c.Assert(s.createVolume("mon0", "policy1", "test", nil), IsNil)

	out, err = s.dockerRun("mon0", false, true, "policy1/test", "sleep 10m")
	c.Assert(err, IsNil, Commentf(out))

	out, err = s.dockerRun("mon1", false, true, "policy1/test", "sleep 10m")
	c.Assert(err, IsNil, Commentf(out))

	out, err = s.dockerRun("mon2", false, true, "policy1/test", "sleep 10m")
	c.Assert(err, IsNil, Commentf(out))
}

func (s *systemtestSuite) TestVolpluginVolmasterDown(c *C) {
	c.Assert(stopVolmaster(s.vagrant.GetNode("mon0")), IsNil)
	c.Assert(stopVolplugin(s.vagrant.GetNode("mon0")), IsNil)
	c.Assert(startVolplugin(s.vagrant.GetNode("mon0")), IsNil)
	c.Assert(startVolmaster(s.vagrant.GetNode("mon0")), IsNil)
	c.Assert(s.createVolume("mon0", "policy1", "test", nil), IsNil)
}

func (s *systemtestSuite) TestVolpluginCleanupSocket(c *C) {
	c.Assert(stopVolplugin(s.vagrant.GetNode("mon0")), IsNil)
	defer c.Assert(startVolplugin(s.vagrant.GetNode("mon0")), IsNil)
	_, err := s.mon0cmd("test -f /run/docker/plugins/volplugin.sock")
	c.Assert(err, NotNil)
}

func (s *systemtestSuite) TestVolpluginFDLeak(c *C) {
	c.Assert(s.restartNetplugin(), IsNil)
	iterations := 2000
	subIterations := 50

	log.Infof("Running %d iterations of `docker volume ls` to ensure no FD exhaustion", iterations)

	errChan := make(chan error, iterations)

	for i := 0; i < iterations/subIterations; i++ {
		go func() {
			for i := 0; i < subIterations; i++ {
				errChan <- s.vagrant.GetNode("mon0").RunCommand("docker volume ls")
			}
		}()
	}

	for i := 0; i < iterations; i++ {
		c.Assert(<-errChan, IsNil)
	}
}

func (s *systemtestSuite) TestVolpluginCrashRestart(c *C) {
	if !cephDriver() {
		c.Skip("only ceph supports runtime parameters")
		return
	}

	c.Assert(s.createVolume("mon0", "policy1", "test", nil), IsNil)
	_, err := s.dockerRun("mon0", false, true, "policy1/test", "sleep 10m")
	c.Assert(err, IsNil)
	c.Assert(stopVolplugin(s.vagrant.GetNode("mon0")), IsNil)
	time.Sleep(45 * time.Second) // this is based on a 5s ttl set at volmaster/volplugin startup
	c.Assert(startVolplugin(s.vagrant.GetNode("mon0")), IsNil)
	c.Assert(waitForVolplugin(s.vagrant.GetNode("mon0")), IsNil)
	c.Assert(s.createVolume("mon1", "policy1", "test", nil), IsNil)
	_, err = s.dockerRun("mon1", false, true, "policy1/test", "sleep 10m")
	c.Assert(err, NotNil)

	c.Assert(stopVolplugin(s.vagrant.GetNode("mon0")), IsNil)
	c.Assert(startVolplugin(s.vagrant.GetNode("mon0")), IsNil)
	c.Assert(waitForVolplugin(s.vagrant.GetNode("mon0")), IsNil)

	_, err = s.volcli("volume runtime upload policy1/test < /testdata/iops1.json")
	c.Assert(err, IsNil)
	time.Sleep(45 * time.Second)
	out, err := s.vagrant.GetNode("mon0").RunCommandWithOutput("sudo cat /sys/fs/cgroup/blkio/blkio.throttle.write_iops_device")
	c.Assert(err, IsNil)
	c.Assert(strings.TrimSpace(out), Not(Equals), "")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var found bool
	for _, line := range lines {
		parts := strings.Split(line, " ")
		c.Assert(len(parts), Equals, 2)
		if parts[1] == "1000" {
			found = true
		}
	}
	c.Assert(found, Equals, true)

	c.Assert(s.createVolume("mon1", "policy1", "test", nil), IsNil)
	_, err = s.dockerRun("mon1", false, true, "policy1/test", "sleep 10m")
	c.Assert(err, NotNil)

	c.Assert(s.clearContainers(), IsNil)

	c.Assert(s.createVolume("mon1", "policy1", "test", nil), IsNil)
	_, err = s.dockerRun("mon1", false, true, "policy1/test", "sleep 10m")
	c.Assert(err, IsNil)
}

func (s *systemtestSuite) TestVolpluginHostLabel(c *C) {
	c.Assert(stopVolplugin(s.vagrant.GetNode("mon0")), IsNil)

	c.Assert(s.vagrant.GetNode("mon0").RunCommandBackground("sudo -E `which volplugin` --host-label quux"), IsNil)

	time.Sleep(10 * time.Millisecond)
	c.Assert(s.createVolume("mon0", "policy1", "foo", nil), IsNil)

	out, err := s.dockerRun("mon0", false, true, "policy1/foo", "sleep 10m")
	c.Assert(err, IsNil)

	defer s.purgeVolume("mon0", "policy1", "foo", true)
	defer s.mon0cmd("docker rm -f " + out)

	ut := &config.UseMount{}

	// we know the pool is rbd here, so cheat a little.
	out, err = s.volcli("use get policy1/foo")
	c.Assert(err, IsNil, Commentf(out))
	c.Assert(json.Unmarshal([]byte(out), ut), IsNil, Commentf(out))
	c.Assert(ut.Hostname, Equals, "quux")
}

func (s *systemtestSuite) TestVolpluginMountPath(c *C) {
	if !cephDriver() {
		c.Skip("Only ceph driver has mounts that work like this (for now)")
		return
	}

	c.Assert(s.uploadGlobal("mountpath_global"), IsNil)
	time.Sleep(time.Second)
	c.Assert(s.createVolume("mon0", "policy1", "test", nil), IsNil)
	_, err := s.dockerRun("mon0", false, true, "policy1/test", "sleep 10m")
	c.Assert(err, IsNil)

	c.Assert(s.vagrant.GetNode("mon0").RunCommand("sudo test -d /mnt/test/rbd/policy1.test"), IsNil)
}
