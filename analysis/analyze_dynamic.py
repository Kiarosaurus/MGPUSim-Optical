#!/usr/bin/env python3
# Análisis de topologías DINÁMICAS (ópticas):
# evolución de la topología y tráfico demandado antes de cada reconfiguración.
#
# Uso:
#   python analyze_dynamic.py TRAZA [--out DIR] [--msg-bytes N]
#                             [--link-bw 1.0] [--window W_SEG]
#
# Modelo de topología: el switch óptico es reactivo - cada evento 'reconfig'
# conecta su enlace origen->destino al terminar (EndTime).
# Un evento 'drain' con enlace identificable lo desconecta.
import argparse
import csv
import os
import sys

# El paquete common/ vive junto a este script
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import pandas as pd  # noqa: E402

from common import matrices, plotting, tracedb, units  # noqa: E402
from common.layout import make_outdir  # noqa: E402


def main():
    ap = argparse.ArgumentParser(
        description="Evolución de la topología óptica + demanda "
                    "pre-reconfiguración (CSV + heatmap) a partir de una "
                    "traza Akita.")
    ap.add_argument("trace", help="ruta a la traza (.sqlite3 o .csv)")
    ap.add_argument("--out", default=None,
                    help="carpeta raíz de resultados (default: "
                         "<carpeta de la traza>/traffic_analysis - los "
                         "resultados quedan junto a la corrida en exps/)")
    ap.add_argument("--msg-bytes", type=float, default=None,
                    help="bytes por DataMsg, matrices de demanda en bytes")
    ap.add_argument("--link-bw", type=float, default=1.0,
                    help="GB/s por enlace conectado, modelo on/off del switch "
                         "(default: 1.0)")
    ap.add_argument("--window", type=float, default=None,
                    help="ventana pre-reconfig en segundos "
                         "(default: desde la reconfig anterior)")
    ap.add_argument("--per-component", action="store_true",
                    help="no colapsar componentes al nivel de GPU")
    args = ap.parse_args()

    # Ruteo dinámico
    if args.out is None:
        args.out = os.path.join(
            os.path.dirname(os.path.abspath(args.trace)), "traffic_analysis")

    df = tracedb.load_trace(args.trace)
    recs = tracedb.reconfig_events(df)
    if recs.empty:
        sys.exit("La traza no contiene eventos 'reconfig'. No se puede analizar (topología dinámica).")

    dem = tracedb.demand_events(df)
    trans = tracedb.transit_events(df)
    # Nivel GPU por defecto: GPU[1].L2Cache[3] -> GPU[1]
    if not args.per_component:
        for ev in (dem, trans):
            if not ev.empty:
                ev["src"] = ev["src"].map(tracedb.collapse_to_gpu)
                ev["dst"] = ev["dst"].map(tracedb.collapse_to_gpu)
    
    # Si el evento reconfig no codifica el enlace en su ID, se infiere del
    # primer fiber_transit del enlace recién conectado
    recs = tracedb.infer_reconfig_links(recs, trans)
    nodes = tracedb.detect_nodes(dem, trans, recs)
    n_gpus = len(nodes)
    # Salida: <out>/<N>_gpus/
    # NOTA: re-analizar la misma traza sobreescribe
    outdir = make_outdir(args.out, n_gpus)
    topo_dir = make_outdir(args.out, n_gpus, "topology")
    rec_dir = make_outdir(args.out, n_gpus, "reconfigurations")
    in_bytes = args.msg_bytes is not None

    print(f"Traza:    {args.trace}")
    print(f"Nodos:    {n_gpus} ({', '.join(nodes)})")
    print(f"Reconfigs: {len(recs)}")
    print(f"Salida:   {outdir}")

    # Lista de reconfiguraciones -> reconfig_events.csv
    with open(os.path.join(outdir, "reconfig_events.csv"), "w",
              newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(["idx", "t_inicio_ns", "t_fin_ns", "duracion_ns",
                    "origen", "destino"])
        for i, r in enumerate(recs.itertuples(index=False), start=1):
            w.writerow([
                i,
                f"{r.t_start * units.S_TO_NS:.3f}",
                f"{r.t_end * units.S_TO_NS:.3f}",
                f"{(r.t_end - r.t_start) * units.S_TO_NS:.3f}",
                r.src or "?", r.dst or "?",
            ])

    # Evolución de la topología: matriz de ancho de banda asignado por enlace
    B = pd.DataFrame(0.0, index=nodes, columns=nodes)
    first_rec = float(recs["t_start"].min())
    if not trans.empty:
        # Enlaces con tránsito antes de la primera reconfig = topología inicial
        pre = trans[trans["time"] < first_rec]
        for s, d in set(zip(pre["src"], pre["dst"])):
            B.loc[s, d] = args.link_bw

    # Snapshot en t=0 -> topology/topo_00_t0.csv/.png
    plotting.save_matrix_csv(B, os.path.join(topo_dir, "topo_00_t0.csv"))
    plotting.save_heatmap(
        B, os.path.join(topo_dir, "topo_00_t0.png"),
        f"Topología inicial (t=0) - {n_gpus} GPUs",
        "Ancho de banda [GB/s]", vmax=args.link_bw)

    # Cambios de topología en orden cronológico:
    # reconfig conecta su enlace al terminar, drain lo desconecta
    drains = tracedb.drain_events(df)
    changes = []
    for r in recs.itertuples(index=False):
        changes.append((float(r.t_end), "connect", r.src, r.dst))
    for d in drains.itertuples(index=False):
        changes.append((float(d.t_start), "disconnect", d.src, d.dst))
    changes.sort(key=lambda c: c[0])

    snap = 0
    for t, action, src, dst in changes:

        # Eventos sin enlace identificable no alteran la matriz
        if src and dst:
            B.loc[src, dst] = args.link_bw if action == "connect" else 0.0
        if action != "connect":
            continue

        # Snapshot tras cada reconfig -> topology/topo_XX_after_reconfig_<t>ns
        snap += 1
        t_ns = t * units.S_TO_NS
        base = os.path.join(
            topo_dir, f"topo_{snap:02d}_after_reconfig_{t_ns:.0f}ns")
        plotting.save_matrix_csv(B, base + ".csv")
        plotting.save_heatmap(
            B, base + ".png",
            f"Topología tras reconfiguración {snap} "
            f"(t={units.format_time(t)})",
            "Ancho de banda [GB/s]", vmax=args.link_bw)

    # Demanda pre-reconfiguración -> reconfigurations/pre_reconfig_XX_<t>ns
    #
    # Ventana (decisión_anterior, decisión_actual]: cerrada en el instante de decisión
    # porque en el modelo reactivo el req_out que dispara la
    # reconfiguración se inyecta exactamente en t_start
    ev_src = dem if not dem.empty else trans
    t_min = float(df["start_time"].min())
    prev_decision = None
    for i, r in enumerate(recs.itertuples(index=False), start=1):
        t_rec = float(r.t_start)
        if args.window is not None:
            # Ventana de ancho fijo pedida por flag
            w_start = max(t_rec - args.window, t_min)
            sub = ev_src[(ev_src["time"] > w_start)
                         & (ev_src["time"] <= t_rec)]
        elif prev_decision is None:
            # Primera reconfig: desde el inicio de la traza, inclusive
            w_start = t_min
            sub = ev_src[(ev_src["time"] >= w_start)
                         & (ev_src["time"] <= t_rec)]
        else:
            w_start = prev_decision
            sub = ev_src[(ev_src["time"] > w_start)
                         & (ev_src["time"] <= t_rec)]
        M = matrices.traffic_matrix(sub, nodes, msg_bytes=args.msg_bytes)
        t_ns = t_rec * units.S_TO_NS
        base = os.path.join(rec_dir, f"pre_reconfig_{i:02d}_t{t_ns:.0f}ns")
        title = (f"Demanda pre-reconfiguración {i} "
                 f"({units.format_time(w_start)} -> {units.format_time(t_rec)}]")
        plotting.save_matrix_and_heatmap(M, base, title, in_bytes=in_bytes)
        prev_decision = t_rec

    print(f"Snapshots de topología: {snap + 1} (t=0 + {snap} reconfigs)")
    print(f"Matrices pre-reconfig:  {len(recs)}")


if __name__ == "__main__":
    main()
