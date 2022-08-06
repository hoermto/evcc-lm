# Load management

## Use cases
- I want to avoid avoid overload on main circuit due to one or more load points in use
- I want to run multiple load points on a restricted circuit (outside load place with limited power supply)
- I generally want to limit the consumption of all or some load points

## Goals
- hierarchical circuits to model the real infrastructure
- allow dedicated meters per circuit if present in installation
- support circuits without meters (usually sub circuits in private installations don't have dedicated meters) or virtually combine multiple loadpoints to a circuit irrespecitve of their physical distribution
- co-exists with existing charge modes (pv, min, now, off)

## Non Goals / Out of Scope
- Load balancing. We use "first come first serve".

## Remarks
- Load management is based on current per phase, while pv modes are based on power
- We don't have phase accurate modeling (installation tracked down for each phase completely. Multiple load points might be connected using phase rotation to avoid shear load)
- As reference we use always the highest loaded phase per circuit. This might lead to not optimal usage of available current and power.
- No load balancing means that we have "first come first serve" situation.
- If a circuit has no meter, the consumption will be evaluated from connected consumers (virtual meter) and sub circuits. If a load point has a real meter configured, the current from phase 1 is used. 
- With virtual meter: If the load point has no meter or does not provide the phase currents, the assigned `chargeCurrent` is used. Chargers or vehicles which do not set the state accordingly after charging, might reserve up to `maxCurrent()` in the circuit, which could prevent other circuits to start charging.

## Implementation requirements
- separate module for simple testing
- isolation using interfaces
- virtual meter for consistent circuit logic

## Implementation
### Circuit
Using circuit struct with 
- max current: highest allowed current
- sub circuits: if sub circuits are connected
- parent circuit: required to evaluate the remaining current in a hierarchy
- meter: get consumption in circuit

Circuit needs to provide on request the `GetRemainingCurrent()`, defined as `maxCurrent - consumption`. Since a circuit might be included in a hierarchy, the upper circuits might have less remaining current than the actual circuit. Therefore the parent circuit reference is required to get the remaining current of the parent.

Consumption is taken from the assigned meter. A meter is either a physical meter provided by config or a virtual meter (see below).

### Virtual Meter
For circuits without real meter the circuit creates a virtual meter. A virtual meter evaluates the consumption using a list of consumers (load points). If a virtual meter is used, it also uses the sub cicruits as consumer to consider their consumtion.
A virtual meter does not know the load of other consumers of this circuit which are eventually connected. This has to be considered in the limit setting.

### Consumer
A consumer as new interface to let a virtual meter get the current consumption. The load point implements this interface. Load point uses `EffectiveCurrent()` to determine the current consumption.
It also helps the load point to adjust the remaining current when setting new limit.

### Load point
The load points hold a optional reference to one circuit. The cicuit is used to get the remaining current of this circuit when setting the new limit.

## Operation
The circuits are generally passive. On `SetLimit()` of a load point the load point checks the circuit for the remaining current at the beginning and adjusts this if its lower than the requested current.
Since the circuit has the total consumption as base for the remaining current, the returned value includes the current consumption of this load point already. Load point adjusts the remaining current using the consumer interface `GetCurrent()`.

In case the remaining current is lower than `MinCurrent`, `SetLimit()` handles this already in the following code.

## Open Points
- currently the Site meter is allowed to be used in circuit to allow site load management. Currently there is no check that this meter is used in more than one circuit.
- slow reacting chargers might cause interferences on charging (on/off changes)
- logs are using loggers with `cc-<ccname>`. Default would be `cc-<id>`, which I consider not user friendly.

## Tasks
[ ] config assistant for circuits

[X] introduce virtual meters to handle circuits w/o real meters

[x] influx values with tag `circuit`