#!/usr/bin/env python3
# Análisis de topologías ESTÁTICAS:
# matriz de tráfico total + matrices por fase (Key Traffic Matrices).
#
# Uso:
#   python analyze_static.py TRAZA [--out DIR] [--msg-bytes N]
#                            [--bins 200] [--theta 0.8]
#
# TRAZA: .sqlite3 (SQLiteTracer de Akita) o .csv (CSVTracer).
# Sin --msg-bytes las matrices están en mensajes. Con el flag, en bytes.
import argparse
import csv
import os
import sys

# El paquete common/ vive junto a este script
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from common import matrices, plotting, tracedb, units  # noqa: E402
from common.layout import make_outdir  # noqa: E402


def main():
    ap = argparse.ArgumentParser(
        description="Matriz de tráfico total + matrices por fase "
                    "(CSV + heatmap) a partir de una traza Akita.")
    ap.add_argument("trace", help="ruta a la traza (.sqlite3 o .csv)")
    ap.add_argument("--out", default=None,
                    help="carpeta raíz de resultados (default: "
                         "<carpeta de la traza>/traffic_analysis - los "
                         "resultados quedan junto a la corrida en exps/)")
    ap.add_argument("--msg-bytes", type=float, default=None,
                    help="bytes por DataMsg, las matrices pasan de mensajes a bytes")
    ap.add_argument("--bins", type=int, default=200,
                    help="ventanas temporales para detección de fases (default: 200)")
    ap.add_argument("--theta", type=float, default=0.8,
                    help="umbral de similitud coseno entre ventanas (default: 0.8)")
    ap.add_argument("--per-component", action="store_true",
                    help="no colapsar componentes al nivel de GPU (matriz "
                         "componente x componente, trazas mgpusim: >100 nodos)")
    args = ap.parse_args()

    # Ruteo dinámico
    if args.out is None:
        args.out = os.path.join(
            os.path.dirname(os.path.abspath(args.trace)), "traffic_analysis")

    df = tracedb.load_trace(args.trace)

    # Eventos de tráfico: fiber_transit si la traza lo registra,
    # si no, demanda req_out->req_in
    ev = tracedb.transit_events(df)
    source = "fiber_transit (tráfico real por enlace)"
    if ev.empty:
        ev = tracedb.demand_events(df)
        source = "req_out->req_in (la traza no registra fiber_transit)"
    if ev.empty:
        sys.exit("La traza no contiene eventos de tráfico (ni fiber_transit ni req_out/req_in).")

    # Nivel GPU por defecto: GPU[1].L2Cache[3] -> GPU[1].
    # La diagonal de la matriz queda con el tráfico interno de cada GPU
    if not args.per_component:
        ev = ev.assign(src=ev["src"].map(tracedb.collapse_to_gpu),
                       dst=ev["dst"].map(tracedb.collapse_to_gpu))

    nodes = tracedb.detect_nodes(ev)
    n_gpus = len(nodes)

    # Salida: <out>/<N>_gpus/
    # NOTA: re-analizar la misma traza sobreescribe
    outdir = make_outdir(args.out, n_gpus)
    in_bytes = args.msg_bytes is not None

    print(f"Traza:    {args.trace}")
    print(f"Fuente:   {source}")
    print(f"Nodos:    {n_gpus} ({', '.join(nodes)})")
    print(f"Salida:   {outdir}")

    # Matriz total: suma de todo el tráfico por enlace -> traffic_total.csv/.png
    total = matrices.traffic_matrix(ev, nodes, msg_bytes=args.msg_bytes)
    plotting.save_matrix_and_heatmap(
        total, os.path.join(outdir, "traffic_total"),
        f"Tráfico total por enlace - {n_gpus} GPUs", in_bytes=in_bytes)

    # las fases se detectan por similitud coseno entre ventanas
    phases = matrices.find_phases( 
        ev, nodes, n_bins=args.bins, theta=args.theta, msg_bytes=args.msg_bytes)

    # Una matriz por fase -> phases/phase_XX_<t0>ns-<t1>ns.csv/.png
    phase_dir = make_outdir(args.out, n_gpus, "phases")
    unit_lbl = "bytes" if in_bytes else "mensajes"
    grand = sum(p["total"] for p in phases) or 1.0
    summary_rows = []
    for k, p in enumerate(phases, start=1):
        t0_ns = p["t_start"] * units.S_TO_NS
        t1_ns = p["t_end"] * units.S_TO_NS
        base = os.path.join(
            phase_dir, f"phase_{k:02d}_{t0_ns:.0f}ns-{t1_ns:.0f}ns")
        title = (f"Fase {k}: {units.format_time(p['t_start'])} -> "
                 f"{units.format_time(p['t_end'])}")
        plotting.save_matrix_and_heatmap(p["matrix"], base, title,
                                         in_bytes=in_bytes)
        summary_rows.append([
            k, f"{t0_ns:.3f}", f"{t1_ns:.3f}", f"{t1_ns - t0_ns:.3f}",
            f"{p['total']:.6g}", f"{100.0 * p['total'] / grand:.2f}",
        ])

    # Resumen de fronteras, duración y volumen -> phases_summary.csv
    with open(os.path.join(outdir, "phases_summary.csv"), "w",
              newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(["fase", "t_inicio_ns", "t_fin_ns", "duracion_ns",
                    f"trafico_{unit_lbl}", "pct_trafico_total"])
        w.writerows(summary_rows)

    print(f"Fases detectadas: {len(phases)}")
    for row in summary_rows:
        print(f"  fase {row[0]}: {row[1]}–{row[2]} ns, "
              f"{row[4]} {unit_lbl} ({row[5]} %)")


if __name__ == "__main__":
    main()
