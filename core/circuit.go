package core

import (
	"fmt"
	"math"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
)

// interface to get the current in use from a consumer
// it is expected to get the max current over all phases
type Consumer interface {
	GetEffectiveCurrent() float64
}

type Circuit struct {
	log    *util.Logger
	uiChan chan<- util.Param

	Name       string  `mapstructure:"name"`       // meaningful name
	MaxCurrent float64 `mapstructure:"maxCurrent"` // the max allowed current of this circuit
	MeterRef   string  `mapstructure:"meter"`      // Charge meter reference

	meterCurrent api.MeterCurrent // optional: meter to determine phase current
	Consumers    []Consumer       // optional: all consumers under management. Used for consumption evaluation if no meter is present
}

// determines current in use. Its defined as consumption by consumers
// if there is a meter, the meter current will define the consumption
// TBD: phase perfect evaluation
func (cc *Circuit) EvaluateConsumption() float64 {
	var current float64
	if cc.meterCurrent != nil {
		current, _ = cc.GetMaxCurrentMeter()
	} else {
		current = cc.GetMaxCurrentConsumers()
	}
	cc.log.TRACE.Printf("actual current: %.1fA", current)
	cc.publish("actualCurrent", current)
	return current
}

// returns aggregated consumption of all consumers
// using their current
func (cc *Circuit) GetMaxCurrentConsumers() float64 {
	cc.log.TRACE.Printf("get current from %d consumers", len(cc.Consumers))
	var current float64
	for _, curLp := range cc.Consumers {
		cc.log.TRACE.Printf("add %.1fA current", curLp.GetEffectiveCurrent())
		current += curLp.GetEffectiveCurrent()
	}
	cc.publish("consumerCurrent", current)
	return current
}

// returns the highest current from meter
// err if no meter set
func (cc *Circuit) GetMaxCurrentMeter() (float64, error) {
	if cc.meterCurrent == nil {
		return 0.0, fmt.Errorf("no meter available")
	}
	cc.log.TRACE.Printf("get current from meter")
	current := 0.0
	var err error
	i1, i2, i3, err := cc.meterCurrent.Currents()
	if err == nil {
		cc.log.DEBUG.Printf("meter currents: %.3gA", []float64{i1, i2, i3})
		cc.publish("meterCurrents", []float64{i1, i2, i3})
		// TODO: phase adjusted handling. Currently we take highest current from all phases
		current = math.Max(math.Max(i1, i2), i3)
	} else {
		cc.log.ERROR.Println(fmt.Errorf("grid meter currents: %v", err))
		return 0.0, fmt.Errorf("error getting meter currents: %v", err)
	}
	return current, nil
}

// returns avaialble current based on limit and consumption
func (cc *Circuit) GetAvailableCurrent() float64 {
	cc.log.TRACE.Printf("get available current")
	// first update current current, mainly to regularly publish the value
	var current = cc.EvaluateConsumption()
	curAvailable := cc.MaxCurrent - current
	if curAvailable < 0.0 {
		cc.log.WARN.Printf("overload detected (%s) - currents: %.1fA, allowed max current is: %.1fA\n", cc.Name, current, cc.MaxCurrent)
		cc.publish("overload", true)
	} else {
		cc.publish("overload", false)
	}
	cc.log.DEBUG.Printf("circuit using %.1fA, %.1fA available", current, curAvailable)
	return curAvailable
}

// creates a circuit with defaults
func NewCircuit(n string, limit float64, mc api.MeterCurrent, l *util.Logger) *Circuit {
	cc := &Circuit{
		Name:         n,
		log:          l,
		MaxCurrent:   limit,
		meterCurrent: mc,
	}
	return cc
}

// creates circuit from config
// using site to get access to the grid meter if configured, see cp.Meter() for details
func NewCircuitFromConfig(log *util.Logger, cp configProvider, other map[string]interface{}, site *Site) (*Circuit, error) {
	cc := NewCircuit("", 0, nil, log)
	if err := util.DecodeOther(other, cc); err != nil {
		return nil, err
	}
	if cc.Name == "" {
		return nil, fmt.Errorf("circuit name must not be emtpy")
	}
	// cc.log = util.NewLogger("cc-" + cc.Name)
	if cc.MeterRef != "" {
		var mtr api.Meter
		if cc.MeterRef == site.Meters.GridMeterRef {
			mtr = site.gridMeter
		} else {
			mtr, _ = cp.Meter(cc.MeterRef)
		}

		if mc, ok := mtr.(api.MeterCurrent); ok {
			cc.meterCurrent = mc
		} else {
			return nil, fmt.Errorf("circuit needs meter with grid current support: %s", cc.MeterRef)
		}
	}
	cc.log.TRACE.Printf("create new circuit: %s, limit: %.1fA", cc.Name, cc.MaxCurrent)
	return cc, nil
}

// publish sends values to UI and databases
func (cc *Circuit) publish(key string, val interface{}) {
	// test helper
	if cc.uiChan == nil {
		return
	}

	key = fmt.Sprintf("circuit-%s_%s", cc.Name, key)

	cc.uiChan <- util.Param{
		Key: key,
		Val: val,
	}
}

// set the UI channel to publish information
func (cc *Circuit) Prepare(uiChan chan<- util.Param) {
	cc.uiChan = uiChan
	cc.publish("maxCurrent", cc.MaxCurrent)
}
