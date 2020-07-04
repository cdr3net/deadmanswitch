package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v2"
)

type EndpointRaw struct {
	Endpoint      string   `yaml:"endpoint"`
	Timeout       string   `yaml:"timeout"`
	Targets       []string `yaml:"targets"`
	RepeatAfter   string   `yaml:"repeatAfter"`
	BackoffFactor float64  `yaml:"backoffFactor"`
}

type Endpoint struct {
	Endpoint      string
	Timeout       time.Duration
	Targets       []string
	RepeatAfter   time.Duration
	BackoffFactor float64
}

func parseEndpoint(e EndpointRaw) Endpoint {
	parsed := Endpoint{
		Endpoint:      e.Endpoint,
		Targets:       e.Targets,
		BackoffFactor: e.BackoffFactor,
	}
	var err error

	parsed.Timeout, err = time.ParseDuration(e.Timeout)
	if err != nil {
		log.Fatalln(err)
	}

	parsed.RepeatAfter, err = time.ParseDuration(e.RepeatAfter)
	if err != nil {
		log.Fatalln(err)
	}

	return parsed
}

func main() {
	var endpoints []Endpoint

	if len(os.Args) >= 2 {
		configContent, err := ioutil.ReadFile(os.Args[1])
		if err != nil {
			log.Fatalln(err)
		}

		var endpointsRaw []EndpointRaw
		err = yaml.Unmarshal(configContent, &endpointsRaw)
		if err != nil {
			log.Fatalln(err)
		}
		for _, e := range endpointsRaw {
			endpoints = append(endpoints, parseEndpoint(e))
		}
	}

	timeoutCounters := promauto.NewCounterVec(
		prometheus.CounterOpts{Name: "deadmanswitch_timeout"}, []string{"endpoint"})
	repeatCounters := promauto.NewCounterVec(
		prometheus.CounterOpts{Name: "deadmanswitch_repeats"}, []string{"endpoint"})
	errorCallingTargetCounters := promauto.NewCounterVec(
		prometheus.CounterOpts{Name: "deadmanswitch_target_errors"}, []string{"endpoint", "target"})

	for _, e := range endpoints {
		ch := make(chan struct{})

		endpoint := e
		timeoutCounter := timeoutCounters.With(map[string]string{"endpoint": e.Endpoint})
		repeatCounter := repeatCounters.With(map[string]string{"endpoint": e.Endpoint})

		var errorCallingTargetCounter []prometheus.Counter
		for _, t := range endpoint.Targets {
			errorCallingTargetCounter = append(
				errorCallingTargetCounter,
				errorCallingTargetCounters.With(map[string]string{"endpoint": e.Endpoint, "target": t}),
			)
		}

		go func() {
			timer := time.NewTimer(endpoint.Timeout)
			lastTimeout := endpoint.Timeout

			onRepeat := false

			for {
				select {
				case <-ch:
					onRepeat = false
					lastTimeout = endpoint.Timeout
					timer.Reset(lastTimeout)

				case <-timer.C:
					var event string
					if onRepeat {
						repeatCounter.Inc()
						event = "Repeat"
					} else {
						timeoutCounter.Inc()
						event = "Timeout"
					}

					onRepeat = true
					lastTimeout = time.Duration(e.BackoffFactor * float64(lastTimeout))
					timer.Reset(lastTimeout)

					log.Println(event + " for endpoint \"" + endpoint.Endpoint + "\"... ")
					for i := range endpoint.Targets {
						msg := "Calling \"" + endpoint.Targets[i] + "\" : "
						_, err := http.Get(endpoint.Targets[i])
						if err != nil {
							errorCallingTargetCounter[i].Inc()
							log.Println(msg, err)
						} else {
							log.Println(msg, "success.")
						}
					}
				}
			}
		}()

		http.HandleFunc("/"+e.Endpoint, func(w http.ResponseWriter, r *http.Request) {
			ch <- struct{}{}
		})
	}

	http.Handle("/metrics", promhttp.Handler())

	log.Fatal(http.ListenAndServe(":8080", nil))

}
