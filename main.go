package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v2"
)

type GetTarget struct {
	URL string `yaml:"url"`
}

type PostBody interface{}

type PostTarget struct {
	URL  string   `yaml:"url"`
	Body PostBody `yaml:"body"`
}

type Target struct {
	Get  *GetTarget  `yaml:"get"`
	Post *PostTarget `yaml:"post"`
}

func (t Target) URL() string {
	if t.Post != nil {
		return t.Post.URL
	} else {
		return t.Get.URL
	}
}

type EndpointRaw struct {
	Endpoint      string   `yaml:"endpoint"`
	Timeout       string   `yaml:"timeout"`
	Targets       []Target `yaml:"targets"`
	RepeatAfter   string   `yaml:"repeatAfter"`
	BackoffFactor float64  `yaml:"backoffFactor"`
}

type Endpoint struct {
	Endpoint      string
	Timeout       time.Duration
	Targets       []Target
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

	metricsEndpoint := "/metrics"
	if m, ok := os.LookupEnv("METRICS_ENDPOINT"); ok {
		metricsEndpoint = m
	}

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
				errorCallingTargetCounters.With(map[string]string{"endpoint": e.Endpoint, "target": t.URL()}),
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
					for i, t := range endpoint.Targets {
						var err error
						var response *http.Response
						var msg string

						if t.Get != nil {

							response, err = http.Get(t.Get.URL)

						} else if t.Post != nil {

							var request *http.Request
							var body string

							switch b := t.Post.Body.(type) {
							case string:
								body = b
							case map[interface{}]interface{}:
								data := url.Values{}
								for k, v := range b {
									switch vv := v.(type) {
									case string:
										data.Add(k.(string), vv)
									case int:
										data.Add(k.(string), strconv.Itoa(vv))
									}
								}
								body = data.Encode()
							default:
								log.Println("Unknown body type for: " + t.Post.URL)
								continue
							}

							request, err = http.NewRequest("POST", t.Post.URL, strings.NewReader(body))
							request.Header.Add("Content-Type", "application/x-www-form-urlencoded")
							request.Header.Add("Content-Length", strconv.Itoa(len(body)))

							if err != nil {
								log.Println("Error creating ", t.Post.URL, err)
								continue
							}

							msg = "POST \"" + t.Post.URL + "\" : "
							response, err = http.DefaultClient.Do(request)
						}

						if err != nil {
							errorCallingTargetCounter[i].Inc()
							log.Println(msg, err)
						} else if response.StatusCode != 200 {
							errorCallingTargetCounter[i].Inc()
							log.Println(msg, response.Body)
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

	http.Handle(metricsEndpoint, promhttp.Handler())

	log.Fatal(http.ListenAndServe(":8080", nil))
}
