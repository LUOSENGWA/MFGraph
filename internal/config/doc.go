// Package config loads server configuration from MFGRAPH_* (and, for a
// module process, MODULE_*) environment variables, optionally layered over
// a configuration file (ParseFile), and checks it for the deployment mode:
// standalone or module.
package config
