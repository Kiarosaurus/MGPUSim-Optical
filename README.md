# MGPUSim: Interconexión Óptica Reconfigurable (PFC)

Fork de [MGPUSim](https://github.com/sarchlab/mgpusim) con una interconexión óptica reconfigurable (`amd/timing/optical/`) y un pipeline de experimentación/análisis propio (`analysis/`).

## Estructura del proyecto

```
mgpusim/
├── amd/
│   ├── timing/optical/        switch óptico reactivo, link, connector
│   └── samples/optical_*      benchmarks ópticos standalone
├── analysis/                  pipeline de ejecución y análisis
│   ├── master_run.sh          orquestador (compila, simula, métricas, tráfico)
│   ├── run_exp.sh             corre una simulación individual
│   ├── procesar_metricas.py   métricas mgpusim, .CSV (unidades estandarizadas)
│   ├── analyze_static.py      matrices de tráfico total + por fases
│   ├── analyze_dynamic.py     evolución de topología + demanda pre-reconfig
│   └── common/                units / tracedb / matrices / plotting / layout
└── exps/                      resultados por benchmark y topología
    └── <benchmark>/<topología>/
        ├── optical_trace_<N>gpus.sqlite3   traza (Daisen + análisis)
        ├── metrics_<N>gpus.sqlite3         métricas
        ├── raw_<N>gpus.csv, resumen_topologia.csv
        └── traffic_analysis/               matrices CSV + heatmaps PNG
```

## Inicio rápido

```bash
bash analysis/master_run.sh
```

Las fases se activan en el bloque de configuración del script (1/0):
`DO_COMPILE`, `DO_SIMULATE`, `DO_METRICS`, `DO_TRAFFIC`, `DO_CLEANUP`.

Con `DO_TRAFFIC=1`, cada traza con eventos de reconfiguración óptica se analiza con `analyze_dynamic.py`; las trazas de topología fija (sin reconfigs) con `analyze_static.py`. Los resultados quedan en la subcarpeta `traffic_analysis/` de la corrida correspondiente dentro de `exps/`, nunca en una carpeta genérica.

Detalle completo de scripts, flags, modelo matemático de fases y convención de unidades: [`analysis/README.md`](analysis/README.md).

## Visualización con Daisen (Gantt global)

```bash
# Terminal 1
cd ../akita/daisen
go run . -sqlite ../../mgpusim/exps/<benchmark>/optical/optical_trace_<N>gpus.sqlite3

# Terminal 2
cd ~/PFC/akita/daisen/static
npm run dev
# http://localhost:3001/global-gantt
```
