// Package registry is hlp's curated vault registry: an embedded vaults.toml
// with typed entries, a
// closed tag vocabulary, search and fuzzy resolution, a join with the live
// vault universe (stats-data merged with hlp.xyz's tracked list, see
// core.MergeVaults), and user-local overrides read from vaults.toml next to
// the user's config.toml.
//
// The registry only says which vaults exist, what they are and why they are
// listed. Entry.Snapshot is a point-in-time record of why an entry was picked
// and must never be shown as current data: live numbers come from the merged
// universe (Row.Live).
//
// The package does no I/O except reading the user override file, and never
// talks to the network.
package registry
