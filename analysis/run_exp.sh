#!/bin/bash

BENCHMARK=$1
TOPOLOGY=$2
GPU_LIST=$3
OUT_DIR=$4
NUM_GPUS=$5
DO_COMPILE=$6
COMPILE_ONLY=${7:-}   # no simula

# Tunables (overridable desde sbatch).
export GOMEMLIMIT="${GOMEMLIMIT:-50GiB}"    # RAM máxima que puede usar el proceso Go
export GOGC="${GOGC:-20}"   # menor GOGC => más GC => menos RAM pero más lento.

# GOMAXPROCS:
# número de threads OS que el scheduler Go puede usar para ejecutar goroutines.
# Pineamos con SLURM_CPUS_PER_TASK para usar todo el CPU asignado por SLURM.
GOMAXPROCS="${GOMAXPROCS:-${SLURM_CPUS_PER_TASK:-}}"
if [ -n "$GOMAXPROCS" ]; then
    export GOMAXPROCS
fi

export PATH="$PATH:/usr/local/go/bin"

# 1. Rutas exactas del código fuente
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd "$SCRIPT_DIR/.." && pwd)

BENCHMARK_DIR="$ROOT_DIR/amd/samples/$BENCHMARK"

if [ ! -d "$BENCHMARK_DIR" ]; then
    echo "     [ERROR] No se encontró el directorio del benchmark en: $BENCHMARK_DIR"
    exit 1
fi

# 2. Compilación: binario -> OUT_DIR.
BIN="$OUT_DIR/${BENCHMARK}_bin"

if [ "$DO_COMPILE" -eq 1 ]; then
    BUILD_TAGS=""
    case "$TOPOLOGY" in
        runner_fattree_pcie) BUILD_TAGS="fattree" ;;
        runner_mesh_nvlink)  BUILD_TAGS="mesh" ;;
    esac

    echo "     [Compilando] $BENCHMARK (tags='${BUILD_TAGS:-default}')..."
    pushd "$BENCHMARK_DIR" > /dev/null || exit 1
    if [ -n "$BUILD_TAGS" ]; then
        go build -tags "$BUILD_TAGS" -o "$BIN"
    else
        go build -o "$BIN"
    fi
    build_rc=$?
    popd > /dev/null || exit 1
    [ "$build_rc" -eq 0 ] || { echo "     [ERROR] build falló: $BENCHMARK"; exit 1; }
fi

[ -x "$BIN" ] || { echo "     [ERROR] no existe binario $BIN."; exit 1; }

# compile-only
[ "$COMPILE_ONLY" = "compile-only" ] && { echo "     [compile-only] listo: $BIN"; exit 0; }

# 3. Argumentos de simulación
#
# Los benchmarks optical_* son binarios standalone: NO parsean los flags del
# runner mgpusim (-timing, -report-all, -trace-vis*).
#
#   optical_deterministic_audit  -> optical_audit.sqlite3       (sin flags)
#   optical_high_load_stress     -> optical_stress.sqlite3      (sin flags)
#   optical_scalable_audit       -> optical_scalable.sqlite3    (acepta -gpus N)
#
# Para el resto usamos los flags estándar del runner.
TRACE_NAME="optical_trace_${NUM_GPUS}gpus"

if [[ "$BENCHMARK" == optical_* ]]; then
    case "$BENCHMARK" in
        optical_scalable_audit)
            SIM_ARGS="-gpus=$NUM_GPUS"
            ;;
        *)
            SIM_ARGS=""
            ;;
    esac
else
    EXTRA_SIM_ARGS="-report-all -trace-vis -trace-vis-db sqlite -trace-vis-db-file ${TRACE_NAME}"
    SIM_ARGS="-timing $EXTRA_SIM_ARGS -gpus=$GPU_LIST"

    # Motor de simulacion paralelo (multi-core). Default ON.
    # PARALLEL=0 para ejecución serial.
    if [ "${PARALLEL:-1}" -eq 1 ]; then
        SIM_ARGS="$SIM_ARGS -parallel"
    fi
fi

# 4. Ejecución
LOG_FILE="$OUT_DIR/run_${NUM_GPUS}gpus.log"

pushd "$OUT_DIR" > /dev/null || exit 1

echo "     [Simulando] Ejecutando $NUM_GPUS GPU(s)..."
./"${BENCHMARK}_bin" $SIM_ARGS > "$LOG_FILE" 2>&1

# 5. Gestión de archivos SQLite post-simulación.
TARGET_TRACE="${TRACE_NAME}.sqlite3"
METRICS_FILE="metrics_${NUM_GPUS}gpus.sqlite3"

if [[ "$BENCHMARK" == optical_* ]]; then
    # Cada benchmark óptico produce un nombre fijo.
    case "$BENCHMARK" in
        optical_deterministic_audit) SRC="optical_audit.sqlite3" ;;
        optical_high_load_stress)    SRC="optical_stress.sqlite3" ;;
        optical_scalable_audit)      SRC="optical_scalable.sqlite3" ;;
    esac

    if [ -f "$SRC" ]; then
        mv -f "$SRC" "$TARGET_TRACE"
        ln -f "$TARGET_TRACE" "$METRICS_FILE" 2>/dev/null || cp -f "$TARGET_TRACE" "$METRICS_FILE"
    else
        echo "     [WARN] No se encontró $SRC en $OUT_DIR — el binary no generó traza."
    fi
elif [ "$TOPOLOGY" == "runner_optical" ]; then
    if [ -f "$TARGET_TRACE" ] && [ ! -f "$METRICS_FILE" ]; then
        ln "$TARGET_TRACE" "$METRICS_FILE" 2>/dev/null || cp "$TARGET_TRACE" "$METRICS_FILE"
    fi
else
    # 5. Renombrar .sqlite3
    # 'ls -t' ordena por fecha de modificación.
    # 'head -n 1' toma solo el primero (el más nuevo).
    NEWEST_SQLITE=$(ls -t *.sqlite3 2>/dev/null | head -n 1)
    if [ -n "$NEWEST_SQLITE" ] && [ "$NEWEST_SQLITE" != "$METRICS_FILE" ]; then
        mv "$NEWEST_SQLITE" "$METRICS_FILE"
    fi
fi

popd > /dev/null || exit 1