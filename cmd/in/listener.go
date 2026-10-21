package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// listenerInfo pins a callback to one private listener belonging to this remote account.
type listenerInfo struct {
	host  string
	inode string
}

// callbackListener inspects only an active request's port, without consuming an accept.
// Linux is the supported remote platform; unavailable or ambiguous evidence is an error.
func callbackListener(host, port string) (*listenerInfo, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("remote callback discovery requires Linux")
	}
	var matches []listenerInfo
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(table)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot inspect the remote callback listener")
		}
		matches = append(matches, listenersIn(string(data), host, port, os.Getuid())...)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("callback address is ambiguous; both loopback families have listeners")
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &matches[0], nil
}

// listenersIn decodes Linux socket-table rows for one account and one loopback endpoint.
func listenersIn(table, host, port string, uid int) []listenerInfo {
	var result []listenerInfo
	s := bufio.NewScanner(strings.NewReader(table))
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 10 || fields[3] != "0A" || fields[7] != strconv.Itoa(uid) {
			continue
		}
		address, p, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		number, err := strconv.ParseUint(p, 16, 16)
		if err != nil || strconv.FormatUint(number, 10) != port {
			continue
		}
		bytes, err := hex.DecodeString(address)
		if err != nil || (len(bytes) != 4 && len(bytes) != 16) {
			continue
		}
		// Linux prints each 32-bit address word in host byte order on supported little-endian targets.
		for i := 0; i < len(bytes); i += 4 {
			bytes[i], bytes[i+3] = bytes[i+3], bytes[i]
			bytes[i+1], bytes[i+2] = bytes[i+2], bytes[i+1]
		}
		ip := net.IP(bytes)
		if !ip.IsLoopback() {
			continue
		}
		if host == "localhost" {
			if !ip.Equal(net.ParseIP("127.0.0.1")) && !ip.Equal(net.ParseIP("::1")) {
				continue
			}
		} else if !ip.Equal(net.ParseIP(host)) {
			continue
		}
		result = append(result, listenerInfo{ip.String(), fields[9]})
	}
	return result
}
