package core

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
)

var gridMeterUsed bool // indicates gridmeter is used already fo a circuit to avoid > 1 usage

type Circuit struct {
	log    *util.Logger
	uiChan chan<- util.Param

	Name       string    `mapstructure:"name"`       // meaningful name, used as reference in lp
	MaxCurrent float64   `mapstructure:"maxCurrent"` // the max allowed current of this circuit
	MeterRef   string    `mapstructure:"meter"`      // Charge meter reference
	Circuits   []Circuit `mapstructure:"circuits"`   // sub circuits as config reference

	parentCircuit *Circuit         // parent circuit reference
	phaseMeter    api.MeterCurrent // meter to determine phase current
	vMeter        *VMeter          // virtual meter if no real meter is used
}

// GetCurrent determines current in use. Implements consumer interface
// TBD: phase perfect evaluation
func (cc *Circuit) MaxPhasesCurrent() (float64, error) {
	var current float64

	i1, i2, i3, err := cc.phaseMeter.Currents()
	if err != nil {
		return 0, fmt.Errorf("failed getting meter currents: %w", err)
	}
	cc.log.DEBUG.Printf("meter currents: %.3gA", []float64{i1, i2, i3})
	cc.publish("meterCurrents", []float64{i1, i2, i3})
	// TODO: phase adjusted handling. Currently we take highest current from all phases
	current = math.Max(math.Max(i1, i2), i3)

	cc.log.DEBUG.Printf("actual current: %.1fA", current)
	cc.publish("actualCurrent", current)
	return current, nil
}

// GetRemainingCurrent avaialble current based on limit and consumption
// checks down up to top level parent
func (cc *Circuit) GetRemainingCurrent() float64 {
	cc.log.TRACE.Printf("get available current")
	// first update current current, mainly to regularly publish the value
	current, err := cc.MaxPhasesCurrent()
	if err != nil {
		cc.log.WARN.Printf("failure getting max phase currents")
		return 0
	}
	curAvailable := cc.MaxCurrent - current
	if curAvailable < 0.0 {
		cc.log.WARN.Printf("overload detected (%s) - currents: %.1fA, allowed max current is: %.1fA\n", cc.Name, current, cc.MaxCurrent)
		cc.publish("overload", true)
	} else {
		cc.publish("overload", false)
	}
	// check parent circuit, return lowest
	if cc.parentCircuit != nil {
		cc.log.TRACE.Printf("get available current from parent: %s", cc.parentCircuit.Name)
		curAvailable = math.Min(curAvailable, cc.parentCircuit.GetRemainingCurrent())
	}
	cc.log.DEBUG.Printf("circuit using %.1fA, %.1fA available", current, curAvailable)
	return curAvailable
}

// NewCircuit a circuit with defaults
func NewCircuit(n string, limit float64, mc api.MeterCurrent, l *util.Logger) *Circuit {
	cc := &Circuit{
		Name:       n,
		log:        l,
		MaxCurrent: limit,
		phaseMeter: mc,
	}
	return cc
}

// NewCircuitFromConfig creates circuit from config
// using site to get access to the grid meter if configured, see cp.Meter() for details
func NewCircuitFromConfig(cp configProvider, other map[string]interface{}, site *Site) (*Circuit, error) {
	var circuit = new(Circuit)
	if err := util.DecodeOther(other, circuit); err != nil {
		return nil, err
	}

	circuit.log = util.NewLogger("cc-" + circuit.Name)
	circuit.log.TRACE.Println("NewCircuitFromConfig()")
	circuit.PrintCircuits(0) // for tracing only
	if err := circuit.InitCircuits(site, cp); err != nil {
		return nil, err
	}
	circuit.PrintCircuits(0) // for tracing only
	circuit.log.TRACE.Printf("created new circuit: %s, limit: %.1fA", circuit.Name, circuit.MaxCurrent)
	circuit.log.TRACE.Println("NewCircuitFromConfig()) end")
	return circuit, nil
}

// InitCircuits initializes circuits in hierarchy incl meters
func (cc *Circuit) InitCircuits(site *Site, cp configProvider) error {
	if cc.Name == "" {
		return fmt.Errorf("circuit name must not be empty")
	}

	cc.log = util.NewLogger("cc-" + cc.Name)
	cc.log.TRACE.Printf("InitCircuits(): %s (%p)", cc.Name, cc)
	if cc.MeterRef != "" {
		var (
			mt  api.Meter
			err error
		)
		if cc.MeterRef == site.Meters.GridMeterRef {
			if gridMeterUsed {
				return fmt.Errorf("grid meter used more in more than one circuit: %s", cc.MeterRef)
			}
			mt = site.gridMeter
			gridMeterUsed = true
			cc.log.TRACE.Printf("add grid meter from site: %s", cc.MeterRef)
		} else {
			mt, err = cp.Meter(cc.MeterRef)
			if err != nil {
				return fmt.Errorf("failed to set meter %s: %w", cc.MeterRef, err)
			}
			cc.log.TRACE.Printf("add separate meter: %s", cc.MeterRef)
		}
		if pm, ok := mt.(api.MeterCurrent); ok {
			cc.phaseMeter = pm
		} else {
			return fmt.Errorf("circuit needs meter with phase current support: %s", cc.MeterRef)
		}
	} else {
		// create virtual meter
		cc.vMeter = NewVMeter(cc.Name)
		cc.phaseMeter = cc.vMeter
	}
	// initialize also included circuits
	if cc.Circuits != nil {
		for ccId := range cc.Circuits {
			cc.log.TRACE.Printf("creating circuit from circuitRef: %s", cc.Circuits[ccId].Name)
			cc.Circuits[ccId].parentCircuit = cc
			if err := cc.Circuits[ccId].InitCircuits(site, cp); err != nil {
				return err
			}
			if vmtr := cc.GetVMeter(); vmtr != nil {
				vmtr.AddConsumer(&cc.Circuits[ccId])
			}
			cc.Circuits[ccId].PrintCircuits(0)
		}
	} else {
		cc.log.TRACE.Printf("no sub circuits")
	}
	cc.log.TRACE.Println("InitCircuits() exit")
	cc.log.INFO.Printf("initialized new circuit: %s, limit: %.1fA", cc.Name, cc.MaxCurrent)
	return nil
}

// GetVMeter returns the meter used in circuit
func (cc *Circuit) GetVMeter() *VMeter {
	return cc.vMeter
}

// PrintCircuits dumps recursively circuit config
// trace output of circuit and subcircuits
func (cc *Circuit) PrintCircuits(indent int) {
	for _, s := range cc.DumpConfig(0, 15) {
		cc.log.TRACE.Println(s)
	}
}

// DumpConfig dumps the current circuit
// returns string array to dump the config
func (cc *Circuit) DumpConfig(indent int, maxIndent int) []string {

	var res []string

	cfgDump := fmt.Sprintf("%s%s:%s meter %s maxCurrent %.1fA",
		strings.Repeat(" ", indent),
		cc.Name,
		strings.Repeat(" ", maxIndent-len(cc.Name)-indent),
		presence[cc.GetVMeter() == nil],
		cc.MaxCurrent,
	)
	res = append(res, cfgDump)

	// cc.Log.TRACE.Printf("%s%s%s: (%p) log: %t, meter: %t, parent: %p\n", strings.Repeat(" ", indent), cc.Name, strings.Repeat(" ", 10-indent), cc, cc.Log != nil, cc.meterCurrent != nil, cc.parentCircuit)
	for id := range cc.Circuits {
		// this does not work (compiler error), but linter requests it. Github wont build ...
		// res = append(res, cc.Circuits[id].DumpConfig(indent+2, maxIndent))
		// hacky work around
		for _, l := range cc.Circuits[id].DumpConfig(indent+2, maxIndent) {
			res = append(res, l)
			// add useless command
			time.Sleep(0)
		}
	}
	return res
}

// GetCircuit returns the circiut with given name, checking all subcircuits
func (cc *Circuit) GetCircuit(n string) *Circuit {
	cc.log.TRACE.Printf("searching for circuit %s in %s", n, cc.Name)
	if cc.Name == n {
		cc.log.TRACE.Printf("found circuit %s (%p)", cc.Name, &cc)
		return cc
	} else {
		for ccId := range cc.Circuits {
			cc.log.TRACE.Printf("start looking in circuit %s (%p)", cc.Circuits[ccId].Name, &cc.Circuits[ccId])
			retCC := cc.Circuits[ccId].GetCircuit(n)
			if retCC != nil {
				cc.log.TRACE.Printf("found circuit %s (%p)", retCC.Name, &retCC)
				return retCC
			}
		}
	}
	cc.log.INFO.Printf("could not find circuit %s", n)
	return nil
}

// publish sends values to UI and databases
func (cc *Circuit) publish(key string, val interface{}) {
	// test helper
	if cc.uiChan == nil {
		return
	}

	cc.uiChan <- util.Param{
		Circuit: &cc.Name,
		Key:     key,
		Val:     val,
	}
}

// Prepare set the UI channel to publish information
func (cc *Circuit) Prepare(uiChan chan<- util.Param) {
	cc.uiChan = uiChan
	cc.publish("name", cc.Name)
	cc.publish("maxCurrent", cc.MaxCurrent)
	if vmtr := cc.GetVMeter(); vmtr != nil {
		cc.publish("virtualMeter", true)
		cc.publish("consumers", len(vmtr.Consumers)-len(cc.Circuits))
	} else {
		cc.publish("virtualMeter", false)
	}
	// initialize sub circuits
	for ccId := range cc.Circuits {
		cc.Circuits[ccId].Prepare(uiChan)
	}
}

// update gets called on every site update call.
// this is used to update the current consumption etc to get published in status and databases
func (cc *Circuit) update() {
	_, _ = cc.MaxPhasesCurrent()
	for ccSub := range cc.Circuits {
		cc.Circuits[ccSub].update()
	}
}
