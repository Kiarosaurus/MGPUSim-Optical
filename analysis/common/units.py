# Estandarización de unidades del pipeline de análisis.
# Convención: todo cálculo interno usa unidades base del simulador (segundos, bytes).
# La conversión a unidades legibles (ns/µs, MB, GB/s) ocurre una sola vez (al escribir CSVs, heatmaps o resúmenes).

BYTES_PER_KB = 1024
BYTES_PER_MB = 1024 ** 2
BYTES_PER_GB = 1024 ** 3

S_TO_NS = 1e9
S_TO_US = 1e6
S_TO_MS = 1e3


def pick_time_unit(seconds):
    # Elige la unidad (ns/us/ms/s) según la magnitud.
    # Retorna (factor, sufijo) tal que valor_legible = segundos * factor.
    s = abs(seconds)
    if s < 1e-6:
        return S_TO_NS, "ns"
    if s < 1e-3:
        return S_TO_US, "us"
    if s < 1.0:
        return S_TO_MS, "ms"
    return 1.0, "s"


def format_time(seconds):
    # Segundos -> string legible (ej: '820 ns').
    factor, unit = pick_time_unit(seconds)
    return f"{seconds * factor:.4g} {unit}"


def pick_bytes_unit(nbytes):
    # Elige la unidad (B/KB/MB/GB) según la magnitud.
    # Retorna (factor, sufijo) tal que valor_legible = bytes * factor.
    b = abs(nbytes)
    if b >= BYTES_PER_GB:
        return 1.0 / BYTES_PER_GB, "GB"
    if b >= BYTES_PER_MB:
        return 1.0 / BYTES_PER_MB, "MB"
    if b >= BYTES_PER_KB:
        return 1.0 / BYTES_PER_KB, "KB"
    return 1.0, "B"


def throughput_gbps(total_bytes, elapsed_seconds):
    # Bytes acumulados + tiempo transcurrido -> GB/s.
    if elapsed_seconds <= 0:
        return 0.0
    return (total_bytes / BYTES_PER_GB) / elapsed_seconds
