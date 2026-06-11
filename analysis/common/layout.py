# Jerarquía de carpetas de resultados del análisis de tráfico.
import os


def make_outdir(root, n_gpus, *sub):
    # <root>/<N>_gpus/[sub...]
    # default <root> = traffic_analysis/ de la corrida exps/ analizada
    # con --out se puede redirigir a cualquier otra raíz.
    path = os.path.join(root, f"{n_gpus}_gpus", *sub)
    # exist_ok: corridas repetidas sobre la misma traza sobreescriben sus archivos
    os.makedirs(path, exist_ok=True)
    return path
