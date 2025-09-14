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
	defaultTimeout = 2 * time.Second
	defaultWorkers = 1000
)

type ScanResult struct {
	Port int
	Open bool
}

type PortScanner struct {
	target  string
	timeout time.Duration
	workers int
	results chan ScanResult
	wg      sync.WaitGroup
}

func NewPortScanner(target string, timeout time.Duration, workers int) *PortScanner {
	return &PortScanner{
		target:  target,
		timeout: timeout,
		workers: workers,
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

	dialer := &net.Dialer{Timeout: ps.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		ps.results <- ScanResult{Port: port, Open: false}
		return
	}

	conn.Close()
	ps.results <- ScanResult{Port: port, Open: true}
}

func (ps *PortScanner) Scan(ports []int) []int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(ports))*ps.timeout)
	defer cancel()

	semaphore := make(chan struct{}, ps.workers)
	var openPorts []int
	done := make(chan bool)

	go func() {
		for result := range ps.results {
			if result.Open {
				openPorts = append(openPorts, result.Port)
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

	sort.Ints(openPorts)
	return openPorts
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	var (
		portFlag    = flag.String("p", "", "端口范围 (例如: 80, 1-1000, 80,81,82, 80-90,443)")
		timeout     = flag.Duration("t", defaultTimeout, "连接超时时间")
		showVersion = flag.Bool("v", false, "显示版本信息")
		showHelp    = flag.Bool("h", false, "显示帮助信息")
		standMode   = flag.Bool("stand", false, "")
		lieMode     = flag.Bool("lie", false, "")
	)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "\n用法: %s <目标主机> [选项]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\n选项:\n")
		fmt.Fprintf(os.Stderr, "  -h\t\t\t显示帮助信息\n")
		fmt.Fprintf(os.Stderr, "  -p string\t\t端口范围 (例如: 80, 1-1000, 80,81,82, 80-90,443)\n")
		fmt.Fprintf(os.Stderr, "  -t duration\t\t连接超时时间 (default 3s)\n")
		fmt.Fprintf(os.Stderr, "  -v\t\t\t显示版本信息\n")
		fmt.Fprintf(os.Stderr, "\n输出模式:\n")
		fmt.Fprintf(os.Stderr, "  --stand\t\t竖向输出：每行一个端口\n")
		fmt.Fprintf(os.Stderr, "  --lie\t\t\t横向输出：逗号分隔的端口列表\n")
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

	// 自动计算worker数量：取端口数量和默认值中的较小值
	workers := defaultWorkers
	if workers > len(ports) {
		workers = len(ports)
	}

	fmt.Printf("scanning %s, ports: %d, workers: %d\n", target, len(ports), workers)

	scanner := NewPortScanner(target, *timeout, workers)

	startTime := time.Now()
	openPorts := scanner.Scan(ports)
	duration := time.Since(startTime)

	if *standMode {
		fmt.Printf("\nscan completed, time: %v\n", duration)
		fmt.Printf("found %d open ports:\n", len(openPorts))
		for _, port := range openPorts {
			fmt.Printf("%d\n", port)
		}
	} else if *lieMode {
		fmt.Printf("\nscan completed, time: %v\n", duration)
		fmt.Printf("found %d open ports:\n", len(openPorts))
		if len(openPorts) > 0 {
			for i, port := range openPorts {
				if i > 0 {
					fmt.Print(",")
				}
				fmt.Print(port)
			}
			fmt.Println()
		}
	} else {
		fmt.Printf("\nscan completed, time: %v\n", duration)
		fmt.Printf("found %d open ports:\n", len(openPorts))

		for _, port := range openPorts {
			fmt.Printf("%d/tcp open\n", port)
		}

		if len(openPorts) == 0 {
			fmt.Println("no open ports found")
		}
	}
}
