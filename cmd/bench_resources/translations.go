// translations.go is the prose the two published pages are written in.
//
// Both languages live here rather than beside the code that uses them, which is
// the arrangement cmd/gen_eval_pages settled on for the same reason: a sentence
// added to one language is visibly missing from the other when the two are in
// one file, and invisible when they are not. It is also the file .golangci.yml
// excludes from the English spell checker, because an English spell checker has
// nothing useful to say about Spanish prose.

package main

// labels is every generated sentence of one page, in that page's language.
//
// The generated blocks are prose, and a Spanish page carrying "8 logical CPUs"
// would be half translated. The alt text belongs here for a stronger reason
// still: it is the whole figure for a reader who cannot see it.
type labels struct {
	Host, Build, Binary, MeasuredOn  string
	LoadSlope, NoSlope               string
	LatencyAt                        string
	MemoryAlt, SeriesAlt, LatencyAlt string
	// NotMeasured is what a region says when the run this page was drawn from
	// produced nothing for it.
	NotMeasured string
	// The host sentence's own words. The values are the machine's and stay as
	// they are; these are the nouns around them, which a Spanish page carrying
	// "8 logical CPUs" would be missing.
	HostCPUs, HostRAM, HostKernel string
}

// enLabels and esLabels are the two.
var (
	enLabels = labels{
		Host:       "Measured on %s",
		Build:      "build %s",
		Binary:     "%.1f MiB on disk",
		MeasuredOn: "on %s",
		LoadSlope: "Fitted across the steps, **%.2f MiB per caller while every caller is working** " +
			"and **%.0f KiB per caller held**, with nothing in flight. " +
			"Reading the first as the second overstates a shared deployment by whatever its requests are carrying.",
		NoSlope: "No per-caller figure is published from this run: the fit came out at or below zero, " +
			"which means the noise between steps was larger than what a caller adds.",
		LatencyAt: "At %d client addresses the median call took **%.0f ms** and the tail **%.0f ms**. " +
			"The load is `%s`, paced at %.0f calls a second per caller, which is a busy client rather than a spinning one.",
		MemoryAlt:   "Resident set per scenario, idle and at peak",
		SeriesAlt:   "Resident set and held heap as the client count grows",
		LatencyAlt:  "Call latency as the client count grows",
		NotMeasured: "_The run this page was drawn from did not measure this._",
		HostCPUs:    "logical CPUs",
		HostRAM:     "GiB RAM",
		HostKernel:  "kernel",
	}
	esLabels = labels{
		Host:       "Medido en %s",
		Build:      "build %s",
		Binary:     "%.1f MiB en disco",
		MeasuredOn: "el %s",
		LoadSlope: "Ajustado sobre los escalones, **%.2f MiB por cliente mientras todos llaman** " +
			"y **%.0f KiB por cliente retenido**, sin nada en vuelo. " +
			"Leer el primero como el segundo sobreestima un despliegue compartido en lo que lleven sus peticiones.",
		NoSlope: "De esta ejecución no se publica ninguna cifra por cliente: el ajuste salió en cero o por debajo, " +
			"que es lo que ocurre cuando el ruido entre escalones es mayor que lo que añade un cliente.",
		LatencyAt: "Con %d direcciones de cliente la llamada mediana tardó **%.0f ms** y la cola **%.0f ms**. " +
			"La carga es `%s`, a %.0f llamadas por segundo y cliente, que es un cliente ocupado y no uno que gira en vacío.",
		MemoryAlt:   "Conjunto residente por escenario, en reposo y en el pico",
		SeriesAlt:   "Conjunto residente y heap retenido según crece el número de clientes",
		LatencyAlt:  "Latencia de las llamadas según crece el número de clientes",
		NotMeasured: "_La ejecución de la que sale esta página no midió esto._",
		HostCPUs:    "CPU lógicas",
		HostRAM:     "GiB de RAM",
		HostKernel:  "kernel",
	}
)
