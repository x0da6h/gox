package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout = 1 * time.Second
	defaultWorkers = 5000
	defaultRetries = 1
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type ScanResult struct {
	Port int
	Open bool
}

type PortScanner struct {
	target  string
	timeout time.Duration
	workers int
	retries int
	results chan ScanResult
	wg      sync.WaitGroup
	mutex   sync.Mutex
}

func NewPortScanner(target string, timeout time.Duration, workers int, retries int) *PortScanner {
	return &PortScanner{
		target:  target,
		timeout: timeout,
		workers: workers,
		retries: retries,
		results: make(chan ScanResult, workers),
	}
}

func parsePortRange(portStr string) ([]int, error) {
	var ports []int
	if strings.HasPrefix(portStr, "-p") {
		portStr = strings.TrimPrefix(portStr, "-p")
	}
	if strings.Contains(portStr, ",") {
		portList := strings.Split(portStr, ",")
		for _, portItem := range portList {
			portItem = strings.TrimSpace(portItem)
			if portItem == "" {
				continue
			}

			if strings.Contains(portItem, "-") {
				rangePorts, err := parseRange(portItem)
				if err != nil {
					return nil, err
				}
				ports = append(ports, rangePorts...)
			} else {
				port, err := strconv.Atoi(portItem)
				if err != nil || port < 1 || port > 65535 {
					return nil, fmt.Errorf("port error")
				}
				ports = append(ports, port)
			}
		}
	} else if strings.Contains(portStr, "-") {
		rangePorts, err := parseRange(portStr)
		if err != nil {
			return nil, err
		}
		ports = append(ports, rangePorts...)
	} else {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("port error")
		}
		ports = append(ports, port)
	}
	return ports, nil
}

func parseRange(rangeStr string) ([]int, error) {
	var ports []int
	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("port error")
	}
	start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || start > end || start < 1 || end > 65535 {
		return nil, fmt.Errorf("port error")
	}
	for i := start; i <= end; i++ {
		ports = append(ports, i)
	}
	return ports, nil
}

func (ps *PortScanner) scanPort(ctx context.Context, port int) {
	defer ps.wg.Done()
	address := fmt.Sprintf("%s:%d", ps.target, port)
	var isOpen bool

	for attempt := 0; attempt <= ps.retries; attempt++ {
		connCtx, cancel := context.WithTimeout(ctx, ps.timeout)
		dialer := &net.Dialer{Timeout: ps.timeout}
		conn, err := dialer.DialContext(connCtx, "tcp", address)
		cancel()

		if err == nil {
			conn.Close()
			isOpen = true
			break
		} else {
			netErr, ok := err.(net.Error)
			if ok && netErr.Timeout() {
				continue
			}
			break
		}
	}

	ps.results <- ScanResult{Port: port, Open: isOpen}
}

func (ps *PortScanner) Scan(ports []int, realtimeOutput bool) []int {
	maxScanTime := 5 * time.Minute
	if time.Duration(len(ports))*ps.timeout < maxScanTime {
		maxScanTime = time.Duration(len(ports)) * ps.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxScanTime)
	defer cancel()

	semaphore := make(chan struct{}, ps.workers)
	var openPorts []int
	done := make(chan bool)

	go func() {
		for result := range ps.results {
			if result.Open {
				ps.mutex.Lock()
				insertIndex := sort.SearchInts(openPorts, result.Port)
				if insertIndex < len(openPorts) && openPorts[insertIndex] == result.Port {
					ps.mutex.Unlock()
					continue
				}
				openPorts = append(openPorts, 0)
				copy(openPorts[insertIndex+1:], openPorts[insertIndex:])
				openPorts[insertIndex] = result.Port
				ps.mutex.Unlock()

				if realtimeOutput {
					fmt.Printf("OPEN %s:%d [TCP]\n", ps.target, result.Port)
				}
			}
		}
		done <- true
	}()

	for _, port := range ports {
		ps.wg.Add(1)
		semaphore <- struct{}{}

		go func(p int) {
			defer func() { <-semaphore }()
			ps.scanPort(ctx, p)
		}(port)
	}

	ps.wg.Wait()
	close(ps.results)
	<-done

	return openPorts
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	var (
		portFlag        = flag.String("p", "", "")
		timeout         = flag.Duration("t", defaultTimeout, "")
		showVersion     = flag.Bool("v", false, "")
		showHelp        = flag.Bool("h", false, "")
		standMode       = flag.Bool("stand", false, "")
		lieMode         = flag.Bool("lie", false, "")
		disableRealtime = flag.Bool("norealtime", false, "")
		retries         = flag.Int("retries", defaultRetries, "")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "\n用法: %s <目标主机> [选项]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\n选项:\n")
		fmt.Fprintf(os.Stderr, "  -h\t\t\t显示帮助信息\n")
		fmt.Fprintf(os.Stderr, "  -p string\t\t端口范围 (例如: 80, 1-1000, 80,81,82, 80-90,443)\n")
		fmt.Fprintf(os.Stderr, "  -t duration\t\t连接超时时间 (default 1s)\n")
		fmt.Fprintf(os.Stderr, "  -v\t\t\t显示版本信息\n")
		fmt.Fprintf(os.Stderr, "  --retries int\t\t连接失败时的重试次数 (default %d)\n", defaultRetries)
		fmt.Fprintf(os.Stderr, "\n输出模式:\n")
		fmt.Fprintf(os.Stderr, "  --stand\t\t竖向输出：每行一个端口\n")
		fmt.Fprintf(os.Stderr, "  --lie\t\t\t横向输出：逗号分隔的端口列表\n")
		fmt.Fprintf(os.Stderr, "  --norealtime\t\t禁用实时输出开放的端口\n")
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("gox-端口扫描器 by x0da6h")
		os.Exit(0)
	}
	if *showHelp {
		flag.Usage()
		os.Exit(0)
	}
	var target string
	var portStr string = *portFlag
	args := flag.Args()
	for i, arg := range args {
		if net.ParseIP(arg) != nil {
			target = arg
		} else if arg == "-p" && i+1 < len(args) {
			portStr = args[i+1]
		} else if arg == "--stand" {
			*standMode = true
		} else if arg == "--lie" {
			*lieMode = true
		} else if arg == "--norealtime" {
			*disableRealtime = true
		} else if !strings.HasPrefix(arg, "-") && target == "" {
			target = arg
		}
	}
	if target == "" {
		fmt.Fprintf(os.Stderr, "error: target not found\n")
		os.Exit(1)
	}
	if portStr == "" {
		fmt.Fprintf(os.Stderr, "error: port required (-p)\n")
		os.Exit(1)
	}
	ports, err := parsePortRange(portStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(ports) == 0 {
		fmt.Fprintf(os.Stderr, "error: no ports specified\n")
		os.Exit(1)
	}
	workers := defaultWorkers
	if workers > len(ports) {
		workers = len(ports)
	}
	if len(ports) > 10000 {
		workers = min(workers, 10000)
	}
	fmt.Printf("scanning %s, ports: %d, workers: %d, timeout: %v, retries: %d\n\n", target, len(ports), workers, *timeout, *retries)
	scanner := NewPortScanner(target, *timeout, workers, *retries)
	startTime := time.Now()
	openPorts := scanner.Scan(ports, !*disableRealtime)
	duration := time.Since(startTime)
	fmt.Printf("\nscan completed, time: %v\n", duration)
	fmt.Printf("found %d open ports\n", len(openPorts))

	if *lieMode {
		if len(openPorts) > 0 {
			fmt.Println()
			for i, port := range openPorts {
				if i > 0 {
					fmt.Print(",")
				}
				fmt.Print(port)
			}
			fmt.Println()
		}
	} else if *standMode {
		if len(openPorts) > 0 {
			fmt.Println()
			for _, port := range openPorts {
				fmt.Printf("%d\n", port)
			}
		}
	} else if *disableRealtime {
		if len(openPorts) > 0 {
			for _, port := range openPorts {
				fmt.Printf("OPEN %s:%d [TCP]\n", target, port)
			}
		} else {
			fmt.Println("no open ports found")
		}
	}
}
