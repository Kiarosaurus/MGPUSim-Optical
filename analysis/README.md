# Pipeline de ejecución y análisis (MGPUSim / Akita óptico)

Todo el tooling de experimentación vive en esta carpeta. La raíz del proyecto
solo contiene el código del simulador (`amd/`) y los resultados (`exps/`).

| Script | Rol |
|---|---|
| `master_run.sh` | Orquestador: compila, simula todos los benchmarks x arreglos de GPUs, extrae métricas y (con `DO_TRAFFIC=1`) genera matrices de tráfico. |
| `run_exp.sh` | Ejecuta UNA simulación (compila el benchmark, corre, renombra los `.sqlite3` a la convención `optical_trace_<N>gpus` / `metrics_<N>gpus`). Invocado por `master_run.sh`. |
| `procesar_metricas.py` | `metrics_<N>gpus.sqlite3` -> CSVs con unidades estandarizadas (µs, bytes, GB/s). |
| `analyze_static.py` | Matriz de tráfico total + matrices por fase (Key Traffic Matrices). |
| `analyze_dynamic.py` | Evolución de topología óptica + demanda pre-reconfiguración. |
| `common/` | `units.py` (estandarización de unidades), `tracedb.py` (lector de trazas), `matrices.py` (matrices + fases), `plotting.py` (heatmaps), `layout.py` (jerarquía de carpetas). |

## Uso

```bash
bash analysis/master_run.sh
```

Las fases se activan editando el bloque de configuración del script (1/0):
`DO_COMPILE`, `DO_SIMULATE`, `DO_METRICS`, `DO_TRAFFIC`, `DO_CLEANUP`.
Para re-analizar sin re-simular: `DO_SIMULATE=0`, `DO_COMPILE=0`, `DO_TRAFFIC=1`.

### `DO_TRAFFIC=1` (análisis de tráfico)

Por cada traza de la corrida (`optical_trace_*gpus.sqlite3` de los benchmarks
ópticos, o `metrics_*gpus.sqlite3` de los runners mgpusim, donde `-trace-vis`
y `-report-all` comparten un único sqlite), `master_run.sh` decide según el
contexto de la simulación:

- traza **con** eventos `reconfig` (interconexión óptica dinámica) ->
  `analyze_dynamic.py`;
- traza **sin** reconfigs (topología fija) -> `analyze_static.py`.

Los analizadores colapsan componentes al nivel de GPU por defecto
(`GPU[1].L2Cache[3]` -> `GPU[1]`; la diagonal de la matriz es el tráfico
interno de cada GPU). `--per-component` conserva el detalle por componente.

### Uso manual de los analizadores

```bash
python analysis/analyze_static.py  exps/<bench>/optical/optical_trace_3gpus.sqlite3 \
    [--out DIR] [--msg-bytes 64] [--bins 200] [--theta 0.8]

python analysis/analyze_dynamic.py exps/<bench>/optical/optical_trace_3gpus.sqlite3 \
    [--out DIR] [--msg-bytes 64] [--link-bw 100] [--window 1e-6]
```

Sin `--out`, los resultados van a `<carpeta de la traza>/traffic_analysis/` —
es decir, quedan junto a la corrida exps/ que los originó.

## Estructura de salida

```
exps/<benchmark>/<topología>/
├── <benchmark>_bin                  binario compilado
├── run_<N>gpus.log                  log de simulación
├── optical_trace_<N>gpus.sqlite3    traza (Daisen + análisis)
├── metrics_<N>gpus.sqlite3          métricas mgpusim
├── raw_<N>gpus.csv                  procesar_metricas.py
├── resumen_topologia.csv            procesar_metricas.py
└── traffic_analysis/
    └── <N>_gpus/
        ├── traffic_total.csv/.png                  (estático)
        ├── phases_summary.csv                      (estático)
        ├── phases/phase_XX_<t0>ns-<t1>ns.csv/.png  (estático)
        ├── reconfig_events.csv                     (dinámico)
        ├── topology/topo_XX_*.csv/.png             (dinámico)
        └── reconfigurations/pre_reconfig_XX_*.csv/.png
```

`<N>_gpus` se autodetecta de los nodos presentes en la traza. Re-analizar la
misma traza sobreescribe los archivos anteriores.

## Convención de unidades

Todo cálculo interno usa unidades base del simulador (**segundos, bytes**).
La conversión a unidades legibles ocurre una sola vez en la salida:

- CSVs de matrices: valores crudos (mensajes, o bytes con `--msg-bytes`).
- Heatmaps: auto-escalados (B/KB/MB/GB; ns/µs/ms en títulos).
- `procesar_metricas.py`: latencias y tiempos en µs (sufijo `_us`),
  tamaños en bytes (`_bytes`), throughput en GB/s.

## Detección de fases (Key Traffic Matrices)

`analyze_static.py` segmenta la traza en fases en tres pasos:

1. Discretiza `[t_min, t_max]` en `--bins` ventanas y arma una matriz de
   tráfico por ventana.
2. Marca fronteras de fase donde: hay un evento `reconfig`/`drain` (frontera
   dura), una ventana vacía es seguida de tráfico (reactivación tras gap), o
   la similitud coseno entre ventanas consecutivas cae bajo `--theta`
   (cambio de patrón espacial).
3. Suma las ventanas de cada segmento y fusiona fases con <1 % del tráfico.

## Modelo dinámico

- **Topología**: cada `reconfig` conecta su enlace al terminar (`EndTime`);
  cada `drain` lo desconecta. Si el evento no codifica el enlace en su ID,
  se infiere del primer `fiber_transit` del enlace (en el switch reactivo los
  mensajes encolados fluyen apenas termina la reconfiguración).
- **Demanda pre-reconfig**: ventana `(decisión_anterior, decisión_actual]`,
  cerrada en el instante de decisión porque el `req_out` que dispara la
  reconfiguración se inyecta exactamente en `t_start`.

## Requisitos

`master_run.sh` usa automáticamente el venv del PFC
(`../venv/bin/python`) si existe; sino `python3` del sistema.

```bash
python -m pip install pandas seaborn matplotlib numpy
```
