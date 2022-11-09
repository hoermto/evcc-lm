package cmd

import (
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/cmd/shutdown"
	"github.com/evcc-io/evcc/core"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/hems"
	"github.com/evcc-io/evcc/provider/javascript"
	"github.com/evcc-io/evcc/provider/mqtt"
	"github.com/evcc-io/evcc/push"
	"github.com/evcc-io/evcc/server"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/server/db/settings"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/locale"
	"github.com/evcc-io/evcc/util/machine"
	"github.com/evcc-io/evcc/util/pipe"
	"github.com/evcc-io/evcc/util/request"
	"github.com/evcc-io/evcc/util/sponsor"
	"github.com/libp2p/zeroconf/v2"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/text/currency"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

var cp = new(ConfigProvider)

func loadConfigFile(conf *config) error {
	err := viper.ReadInConfig()

	if cfgFile = viper.ConfigFileUsed(); cfgFile == "" {
		return err
	}

	log.INFO.Println("using config file:", cfgFile)

	if err == nil {
		if err = viper.UnmarshalExact(&conf); err != nil {
			err = fmt.Errorf("failed parsing config file: %w", err)
		}
	}

	if err == nil {
		logLevel()
	}

	return err
}

func configureEnvironment(cmd *cobra.Command, conf config) (err error) {
	// full http request log
	if cmd.Flags().Lookup(flagHeaders).Changed {
		request.LogHeaders = true
	}

	// setup machine id
	if conf.Plant != "" {
		err = machine.CustomID(conf.Plant)
	}

	// setup sponsorship
	if err == nil && conf.SponsorToken != "" {
		err = sponsor.ConfigureSponsorship(conf.SponsorToken)
	}

	// setup translations
	if err == nil {
		err = locale.Init()
	}

	// setup persistence
	if err == nil && conf.Database.Dsn != "" {
		if flag := cmd.Flags().Lookup(flagSqlite); flag.Changed {
			conf.Database.Type = "sqlite"
			conf.Database.Dsn = flag.Value.String()
		}
		err = configureDatabase(conf.Database)
	}

	// setup mqtt client listener
	if err == nil && conf.Mqtt.Broker != "" {
		err = configureMQTT(conf.Mqtt)
	}

	// setup javascript VMs
	if err == nil {
		err = configureJavascript(conf.Javascript)
	}

	// setup EEBus server
	if err == nil && conf.EEBus != nil {
		err = configureEEBus(conf.EEBus)
	}

	return
}

// configureDatabase configures session database
func configureDatabase(conf dbConfig) error {
	err := db.NewInstance(conf.Type, conf.Dsn)
	if err == nil {
		if err = settings.Init(); err == nil {
			shutdown.Register(func() {
				if err := settings.Persist(); err != nil {
					log.ERROR.Println("cannot save settings:", err)
				}
			})
		}
	}
	return err
}

// configureInflux configures influx database
func configureInflux(conf server.InfluxConfig, loadPoints []loadpoint.API, in <-chan util.Param) {
	influx := server.NewInfluxClient(
		conf.URL,
		conf.Token,
		conf.Org,
		conf.User,
		conf.Password,
		conf.Database,
	)

	// eliminate duplicate values
	dedupe := pipe.NewDeduplicator(30*time.Minute, "vehicleCapacity", "vehicleSoC", "vehicleRange", "vehicleOdometer", "chargedEnergy", "chargeRemainingEnergy")
	in = dedupe.Pipe(in)

	go influx.Run(loadPoints, in)
}

// setup mqtt
func configureMQTT(conf mqttConfig) error {
	log := util.NewLogger("mqtt")

	var err error
	mqtt.Instance, err = mqtt.RegisteredClient(log, conf.Broker, conf.User, conf.Password, conf.ClientID, 1, conf.Insecure, func(options *paho.ClientOptions) {
		topic := fmt.Sprintf("%s/status", strings.Trim(conf.Topic, "/"))
		options.SetWill(topic, "offline", 1, true)
	})
	if err != nil {
		return fmt.Errorf("failed configuring mqtt: %w", err)
	}

	return nil
}

// setup javascript
func configureJavascript(conf map[string]interface{}) error {
	if err := javascript.Configure(conf); err != nil {
		return fmt.Errorf("failed configuring javascript: %w", err)
	}
	return nil
}

// setup HEMS
func configureHEMS(conf typedConfig, site *core.Site, httpd *server.HTTPd) error {
	hems, err := hems.NewFromConfig(conf.Type, conf.Other, site, httpd)
	if err != nil {
		return fmt.Errorf("failed configuring hems: %w", err)
	}

	go hems.Run()

	return nil
}

// setup MDNS
func configureMDNS(conf networkConfig) error {
	host := strings.TrimSuffix(conf.Host, ".local")

	zc, err := zeroconf.RegisterProxy("EV Charge Controller", "_http._tcp", "local.", conf.Port, host, nil, []string{}, nil)
	if err != nil {
		return fmt.Errorf("mDNS announcement: %w", err)
	}

	shutdown.Register(zc.Shutdown)

	return nil
}

// setup EEBus
func configureEEBus(conf map[string]interface{}) error {
	var err error
	if server.EEBusInstance, err = server.NewEEBus(conf); err != nil {
		return fmt.Errorf("failed configuring eebus: %w", err)
	}

	go server.EEBusInstance.Run()
	shutdown.Register(server.EEBusInstance.Shutdown)

	return nil
}

// setup messaging
func configureMessengers(conf messagingConfig, cache *util.Cache) (chan push.Event, error) {
	messageChan := make(chan push.Event, 1)

	messageHub, err := push.NewHub(conf.Events, cache)
	if err != nil {
		return messageChan, fmt.Errorf("failed configuring push services: %w", err)
	}

	for _, service := range conf.Services {
		impl, err := push.NewMessengerFromConfig(service.Type, service.Other)
		if err != nil {
			return messageChan, fmt.Errorf("failed configuring push service %s: %w", service.Type, err)
		}
		messageHub.Add(impl)
	}

	go messageHub.Run(messageChan)

	return messageChan, nil
}

func configureTariffs(conf tariffConfig) (tariff.Tariffs, error) {
	var grid, feedin api.Tariff
	var currencyCode currency.Unit = currency.EUR
	var err error

	if conf.Currency != "" {
		currencyCode = currency.MustParseISO(conf.Currency)
	}

	if conf.Grid.Type != "" {
		grid, err = tariff.NewFromConfig(conf.Grid.Type, conf.Grid.Other)
	}

	if err == nil && conf.FeedIn.Type != "" {
		feedin, err = tariff.NewFromConfig(conf.FeedIn.Type, conf.FeedIn.Other)
	}

	if err != nil {
		err = fmt.Errorf("failed configuring tariff: %w", err)
	}

	tariffs := tariff.NewTariffs(currencyCode, grid, feedin)

	return *tariffs, err
}

func configureSiteLoadpointsCircuits(conf config) (site *core.Site, err error) {
	if err = cp.configure(conf); err == nil {
		var loadPoints []*core.LoadPoint
		loadPoints, err = configureLoadPoints(conf, cp)

		var tariffs tariff.Tariffs
		if err == nil {
			tariffs, err = configureTariffs(conf.Tariffs)
		}

		if err == nil {
			// list of vehicles
			vehicles := lo.MapToSlice(cp.vehicles, func(_ string, v api.Vehicle) api.Vehicle {
				return v
			})

			site, err = configureSite(conf.Site, cp, loadPoints, vehicles, tariffs)
		}

		if err == nil {
			err = configureCircuits(site, loadPoints, cp)
		}
	}

	return site, err
}

func configureSite(conf map[string]interface{}, cp *ConfigProvider, loadPoints []*core.LoadPoint, vehicles []api.Vehicle, tariffs tariff.Tariffs) (*core.Site, error) {
	site, err := core.NewSiteFromConfig(log, cp, conf, loadPoints, vehicles, tariffs)
	if err != nil {
		return nil, fmt.Errorf("failed configuring site: %w", err)
	}

	return site, nil
}

func configureLoadPoints(conf config, cp *ConfigProvider) (loadPoints []*core.LoadPoint, err error) {
	lpInterfaces, ok := viper.AllSettings()["loadpoints"].([]interface{})
	if !ok || len(lpInterfaces) == 0 {
		return nil, errors.New("missing loadpoints")
	}

	for id, lpcI := range lpInterfaces {
		var lpc map[string]interface{}
		if err := util.DecodeOther(lpcI, &lpc); err != nil {
			return nil, fmt.Errorf("failed decoding loadpoint configuration: %w", err)
		}

		log := util.NewLogger("lp-" + strconv.Itoa(id+1))
		lp, err := core.NewLoadPointFromConfig(log, cp, lpc)
		if err != nil {
			return nil, fmt.Errorf("failed configuring loadpoint: %w", err)
		}

		loadPoints = append(loadPoints, lp)
	}

	return loadPoints, nil
}

func configureCircuits(site *core.Site, loadPoints []*core.LoadPoint, cp *ConfigProvider) (err error) {
	ccInterfaces, ok := viper.AllSettings()["circuits"].([]interface{})
	if !ok {
		// no circuits configured
		// in this case, check LPs dont have circuit references
		for _, curLp := range loadPoints {
			if len(curLp.CircuitRef) > 0 {
				return fmt.Errorf("loadpoint %s uses circuit(s), but no circuits are defined", curLp.Title)
			}
		}
		return nil
	}

	for ccId, ccI := range ccInterfaces {
		var ccMap map[string]interface{}
		if err := util.DecodeOther(ccI, &ccMap); err != nil {
			return fmt.Errorf("failed decoding circuit configuration: %w", err)
		}

		ccNew, err := core.NewCircuitFromConfig(cp, ccMap, site)
		if err != nil {
			return fmt.Errorf("failed configuring circuit: %w", err)
		}

		ccNew.PrintCircuits(0)
		site.Circuits = append(site.Circuits, ccNew)
		site.Circuits[ccId].PrintCircuits(0)
	}
	// connect circuits and lps
	for lpId, _ := range loadPoints {
		for ccId, _ := range site.Circuits {
			loadPoints[lpId].CircuitPtr = site.Circuits[ccId].GetCircuit(loadPoints[lpId].CircuitRef)
			if loadPoints[lpId].CircuitPtr != nil {
				loadPoints[lpId].CircuitPtr.GetRemainingCurrent()
				loadPoints[lpId].CircuitPtr.Consumers = append(loadPoints[lpId].CircuitPtr.Consumers, loadPoints[lpId])
				break
			}
		}
		if loadPoints[lpId].CircuitRef != "" && loadPoints[lpId].CircuitPtr == nil {
			// if we are here, no circuit with this name exists
			return fmt.Errorf("loadpoint uses undefined circuit: %s", loadPoints[lpId].CircuitRef)
		}
	}
	return nil
}
