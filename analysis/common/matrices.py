# Construcción, comparación y segmentación de matrices de tráfico src->dst.
import numpy as np
import pandas as pd

# events(src, dst[, time]) -> matriz nodesxnodes (filas=src, cols=dst).
# Valores en nro de mensajes, en bytes si msg_bytes está definido.
def traffic_matrix(events, nodes, msg_bytes=None):
    M = pd.DataFrame(0.0, index=list(nodes), columns=list(nodes))
    if not events.empty:
        counts = events.groupby(["src", "dst"]).size()
        for (s, d), c in counts.items():
            if s in M.index and d in M.columns:
                M.loc[s, d] = float(c)
    if msg_bytes:
        M = M * msg_bytes
    return M

# Similitud coseno entre dos matrices (aplanadas). 1.0 si ambas son cero.
def cosine_similarity(A, B):
    a = A.to_numpy().ravel()
    b = B.to_numpy().ravel()
    na = float(np.linalg.norm(a))
    nb = float(np.linalg.norm(b))
    if na == 0.0 and nb == 0.0:
        return 1.0
    if na == 0.0 or nb == 0.0:
        return 0.0
    return float(np.dot(a, b) / (na * nb))


# Segmenta la traza en fases con patrón espacial de tráfico estable.
# Retorna lista de dicts {t_start, t_end, matrix, total} en orden temporal.
def find_phases(events, nodes, n_bins=200, theta=0.8,
                hard_boundaries=(), min_traffic_frac=0.01, msg_bytes=None):
    if events.empty:
        return []

    # 1. Discretiza [t_min, t_max] en n_bins ventanas y arma una matriz por ventana
    t0 = float(events["time"].min())
    t1 = float(events["time"].max())
    if t1 <= t0:
        t1 = t0 + 1e-12
    edges = np.linspace(t0, t1, n_bins + 1)
    bin_idx = np.clip(
        np.searchsorted(edges, events["time"].to_numpy(), side="right") - 1,
        0, n_bins - 1,
    )

    per_bin = []
    for w in range(n_bins):
        sub = events.iloc[np.nonzero(bin_idx == w)[0]]
        per_bin.append(traffic_matrix(sub, nodes))

    # 2. Fronteras de fase
    boundaries = {0}
    # 2a. Fronteras duras externas (reconfig/drain): parten fase aunque
    #     el patrón de tráfico sea similar antes y después
    for hb in hard_boundaries:
        if t0 < hb < t1:
            w = int(np.clip(
                np.searchsorted(edges, hb, side="right") - 1, 0, n_bins - 1))
            boundaries.add(w)
    for w in range(1, n_bins):
        cur = float(per_bin[w].to_numpy().sum())
        prev = float(per_bin[w - 1].to_numpy().sum())
        if prev == 0.0 and cur > 0.0:
            # 2b. Reactivación tras un gap de inactividad
            boundaries.add(w)
        elif prev > 0.0 and cur > 0.0:
            # 2c. Cambio de patrón espacial aunque el volumen sea continuo
            if cosine_similarity(per_bin[w - 1], per_bin[w]) < theta:
                boundaries.add(w)

    # 3. Suma las ventanas contiguas de cada segmento
    cuts = sorted(boundaries) + [n_bins]
    phases = []
    for a, b in zip(cuts[:-1], cuts[1:]):
        M = per_bin[a].copy()
        for w in range(a + 1, b):
            M += per_bin[w]
        total = float(M.to_numpy().sum())
        # Los segmentos sin tráfico no son fases
        if total == 0.0:
            continue
        phases.append({
            "t_start": float(edges[a]),
            "t_end": float(edges[b]),
            "matrix": M,
            "total": total,
        })

    # 4. Fusiona fases insignificantes (< min_traffic_frac del total) con su
    #    vecina anterior...
    grand_total = sum(p["total"] for p in phases)
    merged = []
    for p in phases:
        if merged and p["total"] < min_traffic_frac * grand_total:
            merged[-1]["matrix"] += p["matrix"]
            merged[-1]["total"] += p["total"]
            merged[-1]["t_end"] = p["t_end"]
        else:
            merged.append(p)
    # ...salvo la primera, que solo puede fusionarse con la siguiente
    if len(merged) >= 2 and merged[0]["total"] < min_traffic_frac * grand_total:
        merged[1]["matrix"] += merged[0]["matrix"]
        merged[1]["total"] += merged[0]["total"]
        merged[1]["t_start"] = merged[0]["t_start"]
        merged = merged[1:]

    # La detección se hizo en mensajes; conversión a bytes recién al final
    if msg_bytes:
        for p in merged:
            p["matrix"] = p["matrix"] * msg_bytes
            p["total"] *= msg_bytes
    return merged
