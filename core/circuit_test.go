package core

import (
	"testing"

	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
)

type testConsumer struct {
	cur float64
}

// interface consumer
func (dc *testConsumer) GetCurrent() float64 {
	return dc.cur
}

type testMeter struct {
	cur float64
}

// interface Meter
func (dm *testMeter) CurrentPower() (float64, error) {
	return dm.cur * (11 / 16), nil
}

// interface MeterCurrents
func (dm *testMeter) Currents() (float64, float64, float64, error) {
	return dm.cur, dm.cur, dm.cur, nil
}

func TestCurrentCircuitConsumers(t *testing.T) {
	// for testing we setup 4 LP with different state, mode and currents
	limit := maxA
	circ := NewCircuit("testCircuit", limit, nil, util.NewLogger("test circuit"))
	assert.NotNilf(t, circ, "circuit not created")

	var consumers []*testConsumer
	for consId := 0; consId < 2; consId++ {
		cons := &testConsumer{
			cur: 0.0,
		}
		consumers = append(consumers, cons)
		circ.Consumers = append(circ.Consumers, cons)
	}

	var curAv float64

	// no LP is consuming
	assert.Equal(t, circ.GetCurrent(), 0.0)

	// asking for current should return the total limit set
	// the Request does not change the LP current ..
	curAv = circ.GetRemainingCurrent()
	assert.Equal(t, curAv, limit)

	// one lp consumes current
	consumers[0].cur = maxA

	// asking for current should now return 0, because 1st lp already has complete consumption
	// the Request does not change the LP current ..
	curAv = circ.GetRemainingCurrent()
	assert.Equal(t, curAv, 0.0)

	// increase the limit of the limiter, now we should get the delta
	circ.MaxCurrent = maxA + 3.0

	curAv = circ.GetRemainingCurrent()
	assert.Equal(t, curAv, 3.0)

	// now reduce limit. there should be no remaining current / negative
	// overload condition
	circ.MaxCurrent = 10.0
	curAv = circ.GetRemainingCurrent()
	assert.LessOrEqual(t, curAv, 0.0)
}

func TestCurrentCircuitMeter(t *testing.T) {
	limit := 20.0
	mtr := &testMeter{cur: 0.0}
	circ := NewCircuit("testCircuit", limit, mtr, util.NewLogger("test circuit"))
	assert.NotNilf(t, circ, "circuit not created")

	var curAv float64
	// no consumption
	assert.Equal(t, circ.GetCurrent(), 0.0)

	// no consumption from meter, return limit
	curAv = circ.GetRemainingCurrent()
	assert.Equal(t, limit, curAv)

	// set some consumption on meter
	mtr.cur = 5
	curAv = circ.GetRemainingCurrent()
	assert.Equal(t, limit-mtr.cur, curAv)

	// simulate production in circuit (negative consumption)
	// available current is limit - consumption
	mtr.cur = -5
	curAv = circ.GetRemainingCurrent()
	assert.Equal(t, limit-mtr.cur, curAv)
}

func TestCurrentCircuitPrio(t *testing.T) {
	limit := 20.0
	mtr := &testMeter{cur: 10.0}
	circ := NewCircuit("testCircuit", limit, mtr, util.NewLogger("test circuit"))
	assert.NotNilf(t, circ, "circuit not created")
	circ.Consumers = append(circ.Consumers, &testConsumer{cur: 16})
	// circuit has meter and consumers. meter has prio
	curAv := circ.GetRemainingCurrent()
	assert.Equal(t, limit-mtr.cur, curAv)
}
