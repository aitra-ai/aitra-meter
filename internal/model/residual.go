package model

// ResidualModelName marks a WindowReport carrying the energy and power of GPUs
// not allocated to any pod on a node. Per-model measurement agents send one
// such report per window alongside the real model reports; the aggregation
// service records it as true idle power. It is not a real model name.
const ResidualModelName = "_idle"
