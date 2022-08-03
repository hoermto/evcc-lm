# Load management

## Use cases
- I want to avoid avoid overload on main circuit due to one or more load points in use
- I want to run multiple load points on a restricted circuit (outside load place with limited power supply)

## Goals
- hierarchical circuits to model the real infrastructure
- allow dedicated meters per circuit if present in installation
- support circuits without meters (usually sub circuits in private installations don't have dedicated meters)
- co-exists with existing charge modes (pv, min, now, off)

## Non Goals / Out of Scope
- Load balancing out of scope, but foreseen

## Remarks
- Load management is based on current per phase, while pv modes are based on power
- We don't have phase accurate modeling (installation tracked down for each phase completely. Multiple load points might be connected using phase rotation to avoid shear load)
- As reference we use always the highest loaded phase per circuit. This might lead to not optimal usage of available current and power.
- No load balancing means that we have "first come first serve" situation.
- If a circuit has no meter, the consumption will be evaluated from connected consumers (load points) and sub circuits. A load point has a meter, and the current from phase 1 is used. If the load point has no meter or does not provide the phase currents, the assigned charge current is used.

## Implementation requirements
- separate module for simple testing
- isolation using interfaces

## Implementation
### Circuit
Using circuit struct with 
- max current: highest allowed current
- sub circuits: if sub circuits are connected
- parent circuit: required to evaluate the remaining current in a hierarchy
- consumers: list of direct attached current consumers

Circuit needs to provide on request the `GetRemainingCurrent()`, defined as `maxCurrent - consumption`. Since a circuit might be included in a hierarchy, the upper circuits might have less remaining current than the actual circuit. Therefore the parent circuit reference is required to get the remaining current of the parent.

Consumption evaluation:
a) using meter if present: this is precise and includes all consumers including sub circuits
b) using sum of all known consumers and sub-circuits (see below)

### Consumers
A consumer as new interface to let a circuit get the current consumption. ATM the load point is the only instance using it. Load point uses `EffectiveCurrent()` to determine this.
It also helps the load point to adjust the remaining current.

When a circuit has no meter, it will use this interface to evaluate the consumption over all consumers. This is a estimation, since there might be more consumers in a circuit which are not controlled by evcc. The configuration of max current should reflect this (make lower than a real fuse to cover additional load).

In a later step this interface could be extended to change the consumption of a consumer to implement load balancing.

### Load point
The load points hold a optional reference to their circuit.

## Operation
The circuits are generally passive. On `SetLimit()` of a load point the load point checks the circuit for the remaining current at the beginning and adjusts this if its lower than the requested current.
Since the circuit has the total consumption as base for the remaining current, the returned value includes the current consumption of this load point already. Load point adjusts the remaining current using the consumer interface `GetCurrent()`.

In case the remaining current is lower than `MinCurrent`, `SetLimit()` handles this already in the following code.

## Open Points
- currently the Site meter is allowed to be used in circuit to allow site load management. Currently there is no check that this meter is used in more than one circuit.
- slow reacting chargers might cause interferences on charging (on/off changes)
- logs are using loggers with `cc-<ccname>`. Default would be `cc-<id>`, which I consider not user friendly.
- publishing values: load points use an array. Circuits atm use also `cc-<ccname>` for each circuit, regardless of the hierarchy. To be be discussed to make it an array.

## Tasks
[ ] config assistant for circuits

[ ] introduce virtual meters to handle circuits w/o real meters