package server

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"sofastack.io/sofa-mosn/pkg/api/v2"
	"sofastack.io/sofa-mosn/pkg/buffer"
	"sofastack.io/sofa-mosn/pkg/log"
	"sofastack.io/sofa-mosn/pkg/network"
	"sofastack.io/sofa-mosn/pkg/types"
)

const testServerName = "test_server"

func setup() {
	handler := NewHandler(&mockClusterManagerFilter{}, &mockClusterManager{})
	initListenerAdapterInstance(testServerName, handler)
}

func tearDown() {
	for _, handler := range listenerAdapterInstance.connHandlerMap {
		handler.StopListeners(context.Background(), true)
	}
}

func TestMain(m *testing.M) {
	setup()
	m.Run()
	tearDown()
}

func baseListenerConfig(addrStr string, name string) *v2.Listener {
	// add a new listener
	addr, _ := net.ResolveTCPAddr("tcp", addrStr)
	return &v2.Listener{
		ListenerConfig: v2.ListenerConfig{
			Name:       name,
			BindToPort: true,
			FilterChains: []v2.FilterChain{
				{
					FilterChainConfig: v2.FilterChainConfig{
						Filters: []v2.Filter{
							{
								Type: "network",
								Config: map[string]interface{}{
									"network": "exists",
								},
							}, // no network filter parsed, but the config still exists for test
						},
					},
					TLSContexts: []v2.TLSConfig{
						v2.TLSConfig{
							Status:     true,
							CACert:     mockCAPEM,
							CertChain:  mockCertPEM,
							PrivateKey: mockKeyPEM,
						},
					},
				},
			},
			StreamFilters: []v2.Filter{
				{
					Type: "stream",
					Config: map[string]interface{}{
						"stream": "exists",
					},
				},
			}, //no stream filters parsed, but the config still exists for test
		},
		Addr: addr,
		PerConnBufferLimitBytes: 1 << 15,
	}
}

// LDS include add\update\delete listener
func TestLDS(t *testing.T) {
	addrStr := "127.0.0.1:8080"
	name := "listener1"
	listenerConfig := baseListenerConfig(addrStr, name)
	// set a network filter do nothing, just for keep the connection not close
	nfcfs := []types.NetworkFilterChainFactory{
		&mockNetworkFilterFactory{},
	}
	if err := GetListenerAdapterInstance().AddOrUpdateListener(testServerName, listenerConfig, nfcfs, nil); err != nil {
		t.Fatalf("add a new listener failed %v", err)
	}
	time.Sleep(time.Second) // wait listener start
	// verify
	// add listener success
	handler := listenerAdapterInstance.defaultConnHandler.(*connHandler)
	if len(handler.listeners) != 1 {
		t.Fatalf("listener numbers is not expected %d", len(handler.listeners))
	}
	ln := handler.FindListenerByName(name)
	if ln == nil {
		t.Fatal("no listener found")
	}
	// use real connection to test
	// tls handshake success
	dialer := &net.Dialer{
		Timeout: time.Second,
	}
	if conn, err := tls.DialWithDialer(dialer, "tcp", addrStr, &tls.Config{
		InsecureSkipVerify: true,
	}); err != nil {
		t.Fatal("dial tls failed", err)
	} else {
		conn.Close()
	}
	// update listener
	// FIXME: update logger
	newListenerConfig := &v2.Listener{
		ListenerConfig: v2.ListenerConfig{
			Name: name, // name should same as the exists listener
			AccessLogs: []v2.AccessLog{
				{},
			},
			FilterChains: []v2.FilterChain{
				{
					FilterChainConfig: v2.FilterChainConfig{
						Filters: []v2.Filter{}, // network filter will not be updated
					},
					TLSContexts: []v2.TLSConfig{ // only tls will be updated
						{
							Status: false,
						},
					},
				},
			},
			StreamFilters: []v2.Filter{}, // stream filter will not be updated
			Inspector:     true,
		},
		Addr: listenerConfig.Addr, // addr should not be changed
		PerConnBufferLimitBytes: 1 << 10,
	}
	if err := GetListenerAdapterInstance().AddOrUpdateListener(testServerName, newListenerConfig, nil, nil); err != nil {
		t.Fatal("update listener failed", err)
	}
	// verify
	// 1. listener have only 1
	if len(handler.listeners) != 1 {
		t.Fatalf("listener numbers is not expected %d", len(handler.listeners))
	}
	// 2. verify config, the updated configs should be changed, and the others should be same as old config
	newLn := handler.FindListenerByName(name)
	cfg := newLn.Config()
	if !(reflect.DeepEqual(cfg.FilterChains[0].TLSContexts[0], newListenerConfig.FilterChains[0].TLSContexts[0]) && //tls is new
		cfg.PerConnBufferLimitBytes == 1<<10 && // PerConnBufferLimitBytes is new
		cfg.Inspector && // inspector is new
		reflect.DeepEqual(cfg.FilterChains[0].Filters, listenerConfig.FilterChains[0].Filters) && // network filter is old
		reflect.DeepEqual(cfg.StreamFilters, listenerConfig.StreamFilters)) { // stream filter is old
		// FIXME: log config is new
		t.Fatal("new config is not expected")
	}
	// FIXME:
	// Logger level is new

	// 3. tls handshake should be failed, because tls is changed to false
	if conn, err := tls.DialWithDialer(dialer, "tcp", addrStr, &tls.Config{
		InsecureSkipVerify: true,
	}); err == nil {
		conn.Close()
		t.Fatal("listener should not be support tls any more")
	}
	// 4.common connection should be success, network filter will not be changed
	if conn, err := net.DialTimeout("tcp", addrStr, time.Second); err != nil {
		t.Fatal("dial listener failed", err)
	} else {
		conn.Close()
	}
	// test delete listener
	if err := GetListenerAdapterInstance().DeleteListener(testServerName, name); err != nil {
		t.Fatal("delete listener failed", err)
	}
	time.Sleep(time.Second) // wait listener close
	if len(handler.listeners) != 0 {
		t.Fatal("handler still have listener")
	}
	// dial should be failed
	if conn, err := net.DialTimeout("tcp", addrStr, time.Second); err == nil {
		conn.Close()
		t.Fatal("listener closed, dial should be failed")
	}
}

func TestUpdateTLS(t *testing.T) {
	addrStr := "127.0.0.1:8081"
	name := "listener2"
	listenerConfig := baseListenerConfig(addrStr, name)
	// set a network filter do nothing, just for keep the connection not close
	nfcfs := []types.NetworkFilterChainFactory{
		&mockNetworkFilterFactory{},
	}
	if err := GetListenerAdapterInstance().AddOrUpdateListener(testServerName, listenerConfig, nfcfs, nil); err != nil {
		t.Fatalf("add a new listener failed %v", err)
	}
	time.Sleep(time.Second) // wait listener start
	tlsCfg := v2.TLSConfig{
		Status: false,
	}
	// tls handleshake success
	dialer := &net.Dialer{
		Timeout: time.Second,
	}
	if conn, err := tls.DialWithDialer(dialer, "tcp", addrStr, &tls.Config{
		InsecureSkipVerify: true,
	}); err != nil {
		t.Fatal("dial tls failed", err)
	} else {
		conn.Close()
	}
	if err := GetListenerAdapterInstance().UpdateListenerTLS(testServerName, name, false, []v2.TLSConfig{tlsCfg}); err != nil {
		t.Fatalf("update tls listener failed %v", err)
	}
	handler := listenerAdapterInstance.defaultConnHandler.(*connHandler)
	newLn := handler.FindListenerByName(name)
	cfg := newLn.Config()
	// verify tls changed
	if !(reflect.DeepEqual(cfg.FilterChains[0].TLSContexts[0], tlsCfg) &&
		cfg.Inspector == false) {
		t.Fatal("update tls config not expected")
	}
	// tls handshake should be failed, because tls is changed to false
	if conn, err := tls.DialWithDialer(dialer, "tcp", addrStr, &tls.Config{
		InsecureSkipVerify: true,
	}); err == nil {
		conn.Close()
		t.Fatal("listener should not be support tls any more")
	}
}

func TestIdleTimeoutAndUpdate(t *testing.T) {
	defer func() {
		buffer.ConnReadTimeout = types.DefaultConnReadTimeout
		defaultIdleTimeout = network.DefaultIdleTimeout
	}()
	log.DefaultLogger.SetLogLevel(log.DEBUG)
	buffer.ConnReadTimeout = time.Second
	defaultIdleTimeout = 3 * time.Second
	addrStr := "127.0.0.1:8082"
	name := "listener3"
	// bas listener config have no idle timeout config, set the default value
	listenerConfig := baseListenerConfig(addrStr, name)
	nfcfs := []types.NetworkFilterChainFactory{
		&mockNetworkFilterFactory{},
	}
	if err := GetListenerAdapterInstance().AddOrUpdateListener(testServerName, listenerConfig, nfcfs, nil); err != nil {
		t.Fatalf("add a new listener failed %v", err)
	}
	time.Sleep(time.Second) // wait listener start

	// 0. test default idle timeout
	func() {
		n := time.Now()
		conn, err := tls.Dial("tcp", addrStr, &tls.Config{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Fatalf("dial failed, %v", err)
		}
		readChan := make(chan error)
		// try read
		go func() {
			buf := make([]byte, 10)
			_, err := conn.Read(buf)
			readChan <- err
		}()
		select {
		case err := <-readChan:
			// connection should be closed by server
			if err != io.EOF {
				t.Fatalf("connection read returns error: %v", err)
			}
			if time.Now().Sub(n) < defaultIdleTimeout {
				t.Fatal("connection closed too quickly")
			}
		case <-time.After(5 * time.Second):
			conn.Close()
			t.Fatal("connection should be closed, but not")
		}
	}()
	// Update idle timeout
	// 1. update as no idle timeout
	noIdle := baseListenerConfig(addrStr, name)
	noIdle.ConnectionIdleTimeout = &v2.DurationConfig{
		Duration: 0,
	}
	if err := GetListenerAdapterInstance().AddOrUpdateListener(testServerName, noIdle, nil, nil); err != nil {
		t.Fatalf("update listener failed, %v", err)
	}
	func() {
		conn, err := tls.Dial("tcp", addrStr, &tls.Config{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Fatalf("dial failed, %v", err)
		}
		readChan := make(chan error)
		// try read
		go func() {
			buf := make([]byte, 10)
			_, err := conn.Read(buf)
			readChan <- err
		}()
		select {
		case err := <-readChan:
			t.Fatalf("receive an error: %v", err)
		case <-time.After(5 * time.Second):
			conn.Close()
		}

	}()
	// 2. update idle timeout with config
	cfgIdle := baseListenerConfig(addrStr, name)
	cfgIdle.ConnectionIdleTimeout = &v2.DurationConfig{
		Duration: 5 * time.Second,
	}
	if err := GetListenerAdapterInstance().AddOrUpdateListener(testServerName, cfgIdle, nil, nil); err != nil {
		t.Fatalf("update listener failed, %v", err)
	}
	func() {
		n := time.Now()
		conn, err := tls.Dial("tcp", addrStr, &tls.Config{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Fatalf("dial failed, %v", err)
		}
		readChan := make(chan error)
		// try read
		go func() {
			buf := make([]byte, 10)
			_, err := conn.Read(buf)
			readChan <- err
		}()
		select {
		case err := <-readChan:
			// connection should be closed by server
			if err != io.EOF {
				t.Fatalf("connection read returns error: %v", err)
			}
			if time.Now().Sub(n) < 5*time.Second {
				t.Fatal("connection closed too quickly")
			}
		case <-time.After(8 * time.Second):
			conn.Close()
			t.Fatal("connection should be closed, but not")
		}
	}()

}

func TestFindListenerByName(t *testing.T) {
	addrStr := "127.0.0.1:8083"
	name := "listener4"
	cfg := baseListenerConfig(addrStr, name)
	if ln := GetListenerAdapterInstance().FindListenerByName(testServerName, name); ln != nil {
		t.Fatal("find listener name failed, expected not found")
	}
	if err := GetListenerAdapterInstance().AddOrUpdateListener(testServerName, cfg, nil, nil); err != nil {
		t.Fatalf("update listener failed, %v", err)
	}
	if ln := GetListenerAdapterInstance().FindListenerByName(testServerName, name); ln == nil {
		t.Fatal("expected find listener, but not")
	}
}
