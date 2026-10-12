//go:build race

package board

// The randomized equivalence tests run fewer steps under -race, where every
// step costs ~20x more; the full count runs in normal test runs.
const equivalenceSteps = 300
