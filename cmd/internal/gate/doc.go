// Package gate holds the command line every gate in this repository takes,
// and the exit statuses they answer with.
//
// A gate here reports in one of three ways, and the split is the same one
// every time: 0 when the run is clean, 1 when -check found something, and 2
// when the command could not do its job at all. The third is the one worth
// having a package for — a gate that cannot run must not read as a gate that
// passed, and that is a rule about every command rather than about any one of
// them.
//
// What is shared is the plumbing, not the decision: [Parse] reads the flags
// and says whether they were accepted, and each command writes its own
// statuses at its own call site, where a reader can see them.
package gate
