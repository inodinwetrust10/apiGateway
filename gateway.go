package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port             string      `yaml:"port"`
	Services         []Service   `yaml:"services"`
	MiddlewareConfig ConfigTypes `yaml:"middleware_config"`
}
type ConfigTypes struct {
	ApiKeyAuth ApiConfig `yaml:"auth_apikey"`
}
type ApiConfig struct {
	ValidKeys []string `yaml:"valid_keys"`
}

type Service struct {
	Name       string   `yaml:"name"`
	Route      string   `yaml:"route_pattern"`
	Urls       []string `yaml:"urls"`
	Middleware []string `yaml:"middleware"`
	proxies    []*httputil.ReverseProxy
	count      uint64
}

type MiddlewareWrapper func(next http.Handler) http.Handler

type Gateway struct {
	Ser      map[string]*Service
	wrappers map[string]MiddlewareWrapper
	apikeys  map[string]bool
}

func (g *Gateway) register(name string, wrapper MiddlewareWrapper) {
	g.wrappers[name] = wrapper
	log.Printf("Registered a middleware %s", name)
}
func (g *Gateway) authAPIKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("X-API-KEY")
		if _, valid := g.apikeys[apiKey]; !valid {
			log.Printf("Authentication failed, API KEY %s not found", apiKey)
			http.Error(w, "Authentication Failed", http.StatusUnauthorized)
			return
		}
		log.Printf("User authenticated")
		next.ServeHTTP(w, r)

	})
}

func NewGateway(cfg *Config) *Gateway {
	gw := &Gateway{
		Ser:      make(map[string]*Service),
		wrappers: make(map[string]MiddlewareWrapper),
		apikeys:  make(map[string]bool),
	}
	for _, key := range cfg.MiddlewareConfig.ApiKeyAuth.ValidKeys {
		gw.apikeys[key] = true
	}
	gw.register("auth_apikey", gw.authAPIKeyMiddleware)

	for i := range cfg.Services {
		currSer := &cfg.Services[i]
		// checked if there is any url provided for our service
		if len(currSer.Urls) == 0 {
			log.Printf("service %s does not have any url defined", currSer.Name)
			return nil
		}
		for i := 0; i < len(currSer.Urls); i++ {
			target, err := url.Parse(currSer.Urls[i])
			if err != nil {
				return nil
			}
			proxy := httputil.NewSingleHostReverseProxy(target)
			currSer.proxies = append(currSer.proxies, proxy)
		}
		gw.Ser[currSer.Route] = currSer
	}

	return gw

}

func (g *Gateway) findService(r *http.Request) *Service {
	for route, service := range g.Ser {
		if strings.HasPrefix(r.URL.Path, route) {
			return service
		}
	}
	return nil
}

func (s *Service) NewProxy() *httputil.ReverseProxy {
	newVal := atomic.AddUint64(&s.count, 1)

	index := int(newVal % uint64(len(s.proxies)))
	return s.proxies[index]
}
func (g *Gateway) handleGateway(w http.ResponseWriter, r *http.Request) {
	service := g.findService(r)
	if service == nil {
		log.Printf("No service found for the url %s", r.URL.Path)
		http.Error(w, "No service found for this url", http.StatusNotFound)
		return
	}

	var curr http.Handler = service.NewProxy()
	for i := len(service.Middleware) - 1; i == 0; i-- {
		middlewareName := service.Middleware[i]
		middleware, ok := g.wrappers[middlewareName]
		if !ok {
			log.Printf("middleware with name %s is not present", middlewareName)
			continue
		}
		curr = middleware(curr)
	}

	log.Printf("forwarding the request of %s path to the service %s with %d middlewares", r.URL.Path, service.Name, len(service.Middleware))
	curr.ServeHTTP(w, r)
}

func loadConfig() *Config {
	var cfg Config
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatal("error loading the config")
	}
	yaml.Unmarshal(data, &cfg)
	return &cfg
}

func main() {
	cfg := loadConfig()
	log.Printf("port %s", cfg.Port)
	gw := NewGateway(cfg)
	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(gw.handleGateway))
	server := &http.Server{
		Addr:    cfg.Port,
		Handler: mux,
	}
	err := server.ListenAndServe()
	if err != nil {
		log.Fatalf("error starting the server on port %s", cfg.Port)
	}
}
