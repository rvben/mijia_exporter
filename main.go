package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"gopkg.in/yaml.v2"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
)

var logger *log.Logger
var metrics = make(map[string]Metric)

type Metric struct {
	Temperature float64
	Humidity    float64
	Voltage     float64
}

func processError(err error) {
	fmt.Println(err)
	os.Exit(2)
}

func readFile(cfg *Config) {
	f, err := os.Open("config.yml")
	if err != nil {
		processError(err)
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(cfg)
	if err != nil {
		processError(err)
	}
}

func printNotification(b []byte) {
	temp := float64(bToInt(b[0:2])) / 100
	hum := float64(b[2])
	volt := float64(bToInt(b[3:5]))
	m := Metric{Temperature: temp, Humidity: hum, Voltage: volt}
	metrics["x"] = m
}

func bToInt(b []byte) int {
	return int(binary.LittleEndian.Uint16(b))
}

type Sensor struct {
	MAC      string `yaml:"mac"`
	Location string `yaml:"location"`
}

type Config struct {
	Sensors []Sensor `yaml:"sensors"`
}

func FindDevice(slice []Sensor, val string) bool {
	for _, item := range slice {
		if item.MAC == val {
			return true
		}
	}
	return false
}

func saveToMetricFunc(ch *chan Metric) func([]byte) {
	return func(b []byte) {
		temp := float64(bToInt(b[0:2])) / 100
		hum := float64(b[2])
		volt := float64(bToInt(b[3:5]))
		m := Metric{Temperature: temp, Humidity: hum, Voltage: volt}
		*ch <- m
	}
}

func getMetric(sensor Sensor) (Metric, error) {
	var sd time.Duration = 20 * time.Second

	logger.Printf("Getting metric for %s, %s", sensor.MAC, sensor.Location)
	filter := func(a ble.Advertisement) bool {
		return strings.ToLower(sensor.MAC) == a.Addr().String()
	}

	ctx := ble.WithSigHandler(context.WithTimeout(context.Background(), sd))
	cln, err := ble.Connect(ctx, filter)
	if err != nil {
		logger.Fatalf("Can't connect : %s", err)
	}

	logger.Printf("Connected : %s", cln.Conn().RemoteAddr())

	done := make(chan struct{})

	// Normally, the connection is disconnected by us after our exploration.
	// However, it can be asynchronously disconnected by the remote peripheral.
	// So we wait(detect) the disconnection in the go routine.
	go func() {
		<-cln.Disconnected()
		fmt.Printf("[ %s ] is disconnected \n", cln.Addr())
		close(done)
	}()

	logger.Printf("Discovering profile...\n")
	p, err := cln.DiscoverProfile(true)
	if err != nil {
		return Metric{}, err
	}

	c := p.FindCharacteristic(ble.NewCharacteristic(ble.MustParse("ebe0ccc17a0a4b0c8a1a6ff2997da3a6")))

	ch := make(chan Metric, 3)

	err = cln.Subscribe(c, false, saveToMetricFunc(&ch))
	if err != nil {
		return Metric{}, err
	}

	var ms = []Metric{<-ch, <-ch, <-ch}

	logger.Printf("Retrieved %d metrics.", len(ms))
	logger.Printf("Ready with getting metric: %#v", ms)
	averageMetric := averageMetrics(ms)
	logger.Printf("Results in metric:  %#v", averageMetric)

	cln.CancelConnection()
	<-done
	return averageMetric, err
}

func averageMetrics(ms []Metric) Metric {
	var totalTemp, totalHum, totalVolt float64
	var amount = float64(len(ms))
	for _, m := range ms {
		totalTemp = totalTemp + m.Temperature
		totalHum = totalHum + m.Humidity
		totalVolt = totalVolt + m.Voltage
	}
	m := Metric{Temperature: totalTemp / amount, Humidity: totalHum / amount, Voltage: totalVolt / amount}
	return m
}

func main() {
	f, err := os.OpenFile("mijia_exporter.log",
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Println(err)
	}
	defer f.Close()
	logger = log.New(f, "", log.LstdFlags)

	var cfg Config
	readFile(&cfg)

	logger.Println("Starting..")
	d, err := linux.NewDevice()
	if err != nil {
		logger.Fatal("Can't create new device:", err)
	}
	logger.Println("Device created.")
	ble.SetDefaultDevice(d)

	recordMetrics(&cfg)

	logger.Println("Starting server at :2112")
	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(":2112", nil)
}

func recordMetrics(cfg *Config) {
	go func() {
		for {
			for _, s := range cfg.Sensors {
				m, err := getMetric(s)
				if err != nil {
					logger.Printf("ERROR [%s]: %s", s.MAC, err)
					continue
				}
				metrics[s.MAC] = m
				temperature.WithLabelValues(s.Location).Set(m.Temperature)
				humidity.WithLabelValues(s.Location).Set(m.Humidity)
				voltage.WithLabelValues(s.Location).Set(m.Voltage)
			}
			time.Sleep(600 * time.Second)
		}
	}()
}

var (
	temperature = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mijia_temperature",
		Help: "The temperature in °C",
	}, []string{
		// To which sensor does these metrics belong?
		"location",
	})
	humidity = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mijia_humidity",
		Help: "The humidity in %",
	}, []string{
		// To which sensor does these metrics belong?
		"location",
	})
	voltage = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mijia_voltage",
		Help: "The voltage in mV",
	}, []string{
		// To which sensor does these metrics belong?
		"location",
	})
)
