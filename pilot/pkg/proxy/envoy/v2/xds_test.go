// Copyright 2018 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package v2_test

import (
	"testing"

	testenv "istio.io/istio/mixer/test/client/env"

	"flag"
	"io/ioutil"
	"os"

	meshconfig "istio.io/api/mesh/v1alpha1"

	"fmt"

	"sync"

	"encoding/json"
	"net/http"

	"istio.io/istio/pilot/pkg/bootstrap"
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pilot/pkg/proxy/envoy/v1/mock"
	"istio.io/istio/tests/util"
)

var (
	// mixer-style test environment, includes mixer and envoy configs.
	testEnv        *testenv.TestSetup
	pilotServer    *bootstrap.Server
	initMutex      sync.Mutex
	initEnvoyMutex sync.Mutex

	envoyStarted = false
	// service1 and service2 are used by mixer tests. Use 'service3' and 'app3' for pilot
	// local tests.

	// 10.10.0.0/24 is service CIDR range

	// 10.0.0.0/9 is instance CIDR range
	app3Ip    = "10.2.0.1"
	gatewayIP = "10.3.0.1"
	ingressIP = "10.3.0.2"
)

// Common code for the xds testing.
// The tests in this package use an in-process pilot using mock service registry and
// envoy, mixer setup using mixer local testing framework.

// Additional servers may be added here.

// One set of pilot/mixer/envoy is used for all tests, similar with the larger integration
// tests in real docker/k8s environments

// Common test environment, including Mixer and Envoy. This is a singleton, the env will be
// used for multiple tests, for local integration testing.
func startEnvoy(t *testing.T) {
	initEnvoyMutex.Lock()
	defer initEnvoyMutex.Unlock()

	if envoyStarted {
		return
	}

	tmplB, err := ioutil.ReadFile(util.IstioSrc + "/tests/testdata/bootstrap_tmpl.json")
	if err != nil {
		t.Fatal("Can't read bootstrap template", err)
	}
	testEnv.EnvoyTemplate = string(tmplB)
	nodeId := sidecarId(app3Ip, "app3")
	testEnv.EnvoyParams = []string{"--service-cluster", "serviceCluster", "--service-node", nodeId, "--v2-config-only"}
	testEnv.EnvoyConfigOpt = map[string]interface{}{
		"NodeID": nodeId,
	}

	// Mixer will push stats every 1 sec
	testenv.SetStatsUpdateInterval(testEnv.MfConfig(), 1)
	if err := testEnv.SetUp(); err != nil {
		t.Fatalf("Failed to setup test: %v", err)
	}
	envoyStarted = true
}

func sidecarId(ip, deployment string) string {
	return fmt.Sprintf("sidecar~%s~%s-644fc65469-96dza.testns~testns.svc.cluster.local", ip, deployment)
}

func gatewayId(ip string) string {
	return fmt.Sprintf("router~%s~istio-gateway-644fc65469-96dzt.istio-system~istio-system.svc.cluster.local", ip)
}

func ingressId(ip string) string {
	return fmt.Sprintf("ingress~%s~istio-ingress-7cd767fcb4-kl6gt.pilot-noauth-system~pilot-noauth-system.svc.cluster.local", ip)
}

// initLocalPilotTestEnv creates a local, in process Pilot with XDSv2 support and a set
// of common test configs. This is a singleton server, reused for all tests in this package.
//
// The server will have a set of pre-defined instances and services, and read CRDs from the
// common tests/testdata directory.
func initLocalPilotTestEnv(t *testing.T) *bootstrap.Server {
	initMutex.Lock()
	defer initMutex.Unlock()
	if pilotServer != nil {
		return pilotServer
	}
	testEnv = testenv.NewTestSetup(testenv.XDSTest, t)
	server := util.EnsureTestServer()
	pilotServer = server

	testEnv.Ports().PilotGrpcPort = uint16(util.MockPilotGrpcPort)
	testEnv.Ports().PilotHTTPPort = uint16(util.MockPilotHTTPPort)
	testEnv.IstioSrc = util.IstioSrc
	testEnv.IstioOut = util.IstioOut

	hostname := "hello.default.svc.cluster.local"
	svc := mock.MakeService(hostname, "10.1.0.0")
	// The default service created by istio/test/util does not have a h2 port.
	// Add a H2 port to test CDS.
	// TODO: move me to discovery.go in istio/test/util
	port := &model.Port{
		Name:                 "h2port",
		Port:                 66,
		Protocol:             model.ProtocolGRPC,
		AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
	}
	svc.Ports = append(svc.Ports, port)
	server.MemoryServiceDiscovery.AddService(hostname, svc)

	// Explicit test service, in the v2 memory registry. Similar with mock.MakeService,
	// but easier to read.
	server.EnvoyXdsServer.MemRegistry.AddService("service3.default.svc.cluster.local", &model.Service{
		Hostname: "service3.default.svc.cluster.local",
		Address:  "10.10.0.1",
		Ports:    testPorts(0),
	})
	server.EnvoyXdsServer.MemRegistry.AddInstance("service3.default.svc.cluster.local", &model.ServiceInstance{
		Endpoint: model.NetworkEndpoint{
			Address: app3Ip,
			Port:    2080,
			ServicePort: &model.Port{
				Name:                 "http-main",
				Port:                 1080,
				Protocol:             model.ProtocolHTTP,
				AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
			},
		},
		Labels:           map[string]string{"version": "v1"},
		AvailabilityZone: "az",
	})
	server.EnvoyXdsServer.MemRegistry.AddInstance("service3.default.svc.cluster.local", &model.ServiceInstance{
		Endpoint: model.NetworkEndpoint{
			Address: gatewayIP,
			Port:    2080,
			ServicePort: &model.Port{
				Name:                 "http-main",
				Port:                 1080,
				Protocol:             model.ProtocolHTTP,
				AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
			},
		},
		Labels:           map[string]string{"version": "v2", "app": "my-gateway-controller"},
		AvailabilityZone: "az",
	})

	server.EnvoyXdsServer.MemRegistry.AddService("istio-ingress.istio-system.svc.cluster.local", &model.Service{
		Hostname: "istio-ingress.istio-system.svc.cluster.local",
		Address:  "10.10.0.2",
		Ports:    testPorts(0),
	})
	server.EnvoyXdsServer.MemRegistry.AddInstance("istio-ingress.istio-system.svc.cluster.local", &model.ServiceInstance{
		Endpoint: model.NetworkEndpoint{
			Address: ingressIP,
			Port:    80,
			ServicePort: &model.Port{
				Name:                 "http-main",
				Port:                 8000,
				Protocol:             model.ProtocolHTTP,
				AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
			},
		},
		Labels:           map[string]string{"version": "v1", "istio": "ingress"},
		AvailabilityZone: "az",
	})

	// Service4 is using port 80, to test that we generate multiple clusters (regression)
	server.EnvoyXdsServer.MemRegistry.AddService("service4", &model.Service{
		Hostname: "service4.default.svc.cluster.local",
		Address:  "10.1.0.4",
		Ports: []*model.Port{
			{
				Name:                 "http-main",
				Port:                 80,
				Protocol:             model.ProtocolHTTP,
				AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
			},
		},
	})

	return server
}

func testPorts(base int) []*model.Port {
	return []*model.Port{
		{
			Name:                 "http-main",
			Port:                 base + 80,
			Protocol:             model.ProtocolHTTP,
			AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
		}, {
			Name:                 "http-status",
			Port:                 base + 81,
			Protocol:             model.ProtocolHTTP,
			AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
		}, {
			Name:                 "custom",
			Port:                 base + 90,
			Protocol:             model.ProtocolTCP,
			AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
		}, {
			Name:                 "mongo",
			Port:                 base + 100,
			Protocol:             model.ProtocolMongo,
			AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
		},
		{
			Name:                 "redis",
			Port:                 base + 110,
			Protocol:             model.ProtocolRedis,
			AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
		}, {
			Name:                 "h2port",
			Port:                 base + 66,
			Protocol:             model.ProtocolGRPC,
			AuthenticationPolicy: meshconfig.AuthenticationPolicy_INHERIT,
		}}
}

// Test XDS with real envoy and with mixer.
func TestEnvoy(t *testing.T) {
	initLocalPilotTestEnv(t)
	startEnvoy(t)
	// Make sure tcp port is ready before starting the test.
	testenv.WaitForPort(testEnv.Ports().TCPProxyPort)

	statsURL := fmt.Sprintf("http://localhost:%d/stats?format=json", testEnv.Ports().AdminPort)
	res, err := http.Get(statsURL)
	if err != nil {
		t.Fatal("Failed to get stats, envoy not started")
	}
	statsBytes, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Fatal("Failed to get stats, envoy not started")
	}

	statsMap := stats2map(statsBytes)

	if statsMap["cluster_manager.cds.update_success"] < 1 {
		t.Error("Failed cds update")
	}
	// Other interesting values for CDS: cluster_added: 19, active_clusters
	// cds.update_attempt: 2, cds.update_rejected, cds.version

	if statsMap["cluster.outbound|custom||service3.default.svc.cluster.local.update_success"] < 1 {
		t.Error("Failed sds updates")
	}

	if statsMap["cluster.xds-grpc.update_failure"] > 0 {
		t.Error("GRPC update failure")
	}

	if statsMap["listener_manager.lds.update_rejected"] > 0 {
		t.Error("LDS update failure")
	}
	if statsMap["listener_manager.lds.update_success"] < 1 {
		t.Error("LDS update failure")
	}

}

// EnvoyStat is used to parse envoy stats
type EnvoyStat struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

// stats2map parses envoy stats.
func stats2map(stats []byte) map[string]int {
	s := struct {
		Stats []EnvoyStat `json:"stats"`
	}{}
	_ = json.Unmarshal(stats, &s)
	m := map[string]int{}
	for _, stat := range s.Stats {
		m[stat.Name] = stat.Value
	}
	return m
}

func TestMain(m *testing.M) {
	flag.Parse()
	defer func() {
		if testEnv != nil {
			testEnv.TearDown()
		}
	}()

	// Run all tests.
	os.Exit(m.Run())
}
