#!/bin/bash

BENCHMARK=$1
TOPOLOGY=$2
GPU_LIST=$3
OUT_DIR=$4
NUM_GPUS=$5
DO_COMPILE=$6

export GOMEMLIMIT="50GiB"
export GOGC=20

# 1. Rutas exactas del código fuente
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
BENCHMARK_DIR="$SCRIPT_DIR/amd/samples/$BENCHMARK"

if [ ! -d "$BENCHMARK_DIR" ]; then
    echo "     [ERROR] No se encontró el directorio del benchmark en: $BENCHMARK_DIR"
    exit 1
fi

# 2. Compilación: Guardamos el binario resultante en la carpeta OUT_DIR
if [ "$DO_COMPILE" -eq 1 ]; then
    echo "     [Compilando] $BENCHMARK (Topología: $TOPOLOGY)..."
    pushd "$BENCHMARK_DIR" > /dev/null || exit 1
    go build -o "$OUT_DIR/${BENCHMARK}_bin"
    popd > /dev/null || exit 1
fi

# 3. Argumentos (el único EXTRA_SIM_ARGS es -report-all)
EXTRA_SIM_ARGS="-report-all"

if [ "$BENCHMARK" == "matrixmultiplication" ]; then
    #SIM_ARGS="-timing $EXTRA_SIM_ARGS -x 2048 -y 2048 -z 2048 -gpus=$GPU_LIST"
    SIM_ARGS="-timing $EXTRA_SIM_ARGS -gpus=$GPU_LIST"
elif [ "$BENCHMARK" == "simpleconvolution" ]; then
    #SIM_ARGS="-timing $EXTRA_SIM_ARGS -width 2048 -height 2048 -gpus=$GPU_LIST"
    SIM_ARGS="-timing $EXTRA_SIM_ARGS -gpus=$GPU_LIST"
else
    SIM_ARGS="-timing $EXTRA_SIM_ARGS -gpus=$GPU_LIST"
fi

# 4. Ejecución (Se ejecuta desde OUT_DIR para que metrics.sqlite3 se guarde ahí)
LOG_FILE="$OUT_DIR/run_${NUM_GPUS}gpus.log"

pushd "$OUT_DIR" > /dev/null || exit 1

echo "     [Simulando] Ejecutando $NUM_GPUS GPU(s)..."
./"${BENCHMARK}_bin" $SIM_ARGS > "$LOG_FILE" 2>&1


# 5. Renombrar .sqlite3
# 'ls -t' ordena por fecha de modificación.
# 'head -n 1' toma solo el primero (el más nuevo).
NEWEST_SQLITE=$(ls -t *.sqlite3 2>/dev/null | head -n 1)

if [ -n "$NEWEST_SQLITE" ] && [ "$NEWEST_SQLITE" != "metrics_${NUM_GPUS}gpus.sqlite3" ]; then
    mv "$NEWEST_SQLITE" "metrics_${NUM_GPUS}gpus.sqlite3"
fi

popd > /dev/null || exit 1