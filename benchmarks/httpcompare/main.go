// Command httpcompare measures completed HTTP responses from the bundled Fango
// server and a plain net/http server. It is an opt-in, same-host comparison.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const request = "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"
const expectedBody = "Hello, world"

type observation struct {
	Server            string  `json:"server"`
	Round             int     `json:"round"`
	Connections       int     `json:"connections"`
	Requests          int     `json:"requests"`
	ElapsedNS         int64   `json:"elapsed_ns"`
	MeanUS            float64 `json:"mean_us"`
	P50US             float64 `json:"p50_us"`
	P95US             float64 `json:"p95_us"`
	P99US             float64 `json:"p99_us"`
	RequestsPerSecond float64 `json:"requests_per_second"`
	LatenciesNS       []int64 `json:"latencies_ns"`
}

type report struct {
	Started      time.Time     `json:"started"`
	GoVersion    string        `json:"go_version"`
	Platform     string        `json:"platform"`
	Procs        int           `json:"gomaxprocs"`
	SourceSHA256 string        `json:"source_sha256"`
	Warmup       int           `json:"warmup_per_connection"`
	Observations []observation `json:"observations"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	requests := flag.Int("requests", 2000, "measured requests per server and concurrency setting, per round")
	warmup := flag.Int("warmup", 50, "warmup requests on each connection before timing")
	rounds := flag.Int("rounds", 3, "paired rounds (server order alternates)")
	procs := flag.Int("procs", 4, "GOMAXPROCS for both servers")
	serverGOGC := flag.String("server-gogc", "", "optional GOGC value for both server processes")
	out := flag.String("out", "", "optional JSON evidence file (includes raw latencies)")
	flag.Parse()
	if *requests < 16 || *warmup < 0 || *rounds < 1 || *procs < 1 {
		return fmt.Errorf("require requests >= 16, warmup >= 0, rounds >= 1, procs >= 1")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, path := range []string{"go.mod", "benchmarks/httpcompare/server.fango"} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			return fmt.Errorf("run from repository root: %w", err)
		}
	}
	fangoSource, err := os.ReadFile(filepath.Join(root, "benchmarks/httpcompare/server.fango"))
	if err != nil {
		return err
	}
	goSource, err := os.ReadFile(filepath.Join(root, "benchmarks/httpcompare/goserver/main.go"))
	if err != nil {
		return err
	}
	hash := sha256.Sum256(append(fangoSource, goSource...))
	work, err := os.MkdirTemp("", "fango-httpcompare-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	fangoCLI := filepath.Join(work, "fango")
	fangoServer := filepath.Join(work, "fango-server")
	goServer := filepath.Join(work, "go-server")
	if err := command(root, nil, "go", "build", "-o", fangoCLI, "./cmd/fango"); err != nil {
		return err
	}
	buildEnv := append(os.Environ(), "FANGO_ROOT="+root, "FANGO_BUILD_DIR="+filepath.Join(work, "build"))
	if err := command(root, buildEnv, fangoCLI, "build", "-o", fangoServer, "benchmarks/httpcompare/server.fango"); err != nil {
		return err
	}
	if err := command(root, nil, "go", "build", "-o", goServer, "./benchmarks/httpcompare/goserver"); err != nil {
		return err
	}

	settings := []int{1, 16}
	record := report{Started: time.Now(), GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, Procs: *procs, SourceSHA256: hex.EncodeToString(hash[:]), Warmup: *warmup}
	fmt.Printf("Go %s, %s, GOMAXPROCS=%d; %d requests per case, %d rounds\n", record.GoVersion, record.Platform, *procs, *requests, *rounds)
	fmt.Println("server  clients round  mean µs  p50 µs  p95 µs  p99 µs  req/s")
	for round := 1; round <= *rounds; round++ {
		for _, clients := range settings {
			servers := []struct{ name, binary string }{{"fango", fangoServer}, {"go", goServer}}
			if round%2 == 0 {
				servers[0], servers[1] = servers[1], servers[0]
			}
			for _, server := range servers {
				port, err := freePort()
				if err != nil {
					return err
				}
				stop, err := startServer(root, server.binary, port, *procs, *serverGOGC)
				if err != nil {
					return err
				}
				sample, measureErr := measure(server.name, round, port, clients, *requests, *warmup)
				stopErr := stop()
				if measureErr != nil {
					return measureErr
				}
				if stopErr != nil {
					return stopErr
				}
				record.Observations = append(record.Observations, sample)
				fmt.Printf("%-6s %7d %5d %8.1f %8.1f %8.1f %8.1f %8.0f\n", sample.Server, sample.Connections, sample.Round, sample.MeanUS, sample.P50US, sample.P95US, sample.P99US, sample.RequestsPerSecond)
			}
		}
	}
	if *out != "" {
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, append(data, '\n'), 0644); err != nil {
			return err
		}
		fmt.Printf("Evidence: %s\n", *out)
	}
	return nil
}

func command(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, output)
	}
	return nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return port, listener.Close()
}

func startServer(dir, binary string, port, procs int, serverGOGC string) (func() error, error) {
	cmd := exec.Command(binary, strconv.Itoa(port))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOMAXPROCS="+strconv.Itoa(procs))
	if serverGOGC != "" {
		cmd.Env = append(cmd.Env, "GOGC="+serverGOGC)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop := func() error {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		_ = cmd.Wait()
		return nil
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return stop, nil
		}
		if time.Now().After(deadline) {
			_ = stop()
			return nil, fmt.Errorf("starting %s: %w; output: %s", binary, err, output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func measure(server string, round, port, clients, requests, warmup int) (observation, error) {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	type session struct {
		conn   net.Conn
		reader *bufio.Reader
		count  int
	}
	sessions := make([]session, clients)
	defer func() {
		for _, session := range sessions {
			if session.conn != nil {
				_ = session.conn.Close()
			}
		}
	}()
	for index := range sessions {
		conn, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err != nil {
			return observation{}, err
		}
		sessions[index] = session{conn: conn, reader: bufio.NewReader(conn), count: requests / clients}
		if index < requests%clients {
			sessions[index].count++
		}
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		for n := 0; n < warmup; n++ {
			if err := exchange(conn, sessions[index].reader); err != nil {
				return observation{}, fmt.Errorf("%s warmup: %w", server, err)
			}
		}
	}
	timings := make([][]int64, clients)
	errors := make(chan error, clients)
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(clients)
	for index, entry := range sessions {
		go func(index int, session session) {
			defer done.Done()
			samples := make([]int64, 0, session.count)
			<-start
			for n := 0; n < session.count; n++ {
				before := time.Now()
				if err := exchange(session.conn, session.reader); err != nil {
					errors <- err
					return
				}
				samples = append(samples, time.Since(before).Nanoseconds())
			}
			timings[index] = samples
		}(index, entry)
	}
	before := time.Now()
	close(start)
	done.Wait()
	elapsed := time.Since(before)
	close(errors)
	if err := <-errors; err != nil {
		return observation{}, fmt.Errorf("%s measurement: %w", server, err)
	}
	values := make([]int64, 0, requests)
	var total int64
	for _, group := range timings {
		for _, ns := range group {
			values = append(values, ns)
			total += ns
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	percentile := func(p int) float64 { return float64(values[(len(values)*p+99)/100-1]) / 1000 }
	return observation{Server: server, Round: round, Connections: clients, Requests: requests,
		ElapsedNS: elapsed.Nanoseconds(), MeanUS: float64(total) / float64(requests) / 1000,
		P50US: percentile(50), P95US: percentile(95), P99US: percentile(99),
		RequestsPerSecond: float64(requests) / elapsed.Seconds(), LatenciesNS: values}, nil
}

func exchange(conn net.Conn, reader *bufio.Reader) error {
	if _, err := io.WriteString(conn, request); err != nil {
		return err
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return err
	}
	if response.StatusCode != 200 || string(body) != expectedBody {
		return fmt.Errorf("response %s, body %q", response.Status, body)
	}
	return nil
}
