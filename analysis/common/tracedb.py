# Lector canónico de trazas Akita/Daisen.
# Acepta .sqlite3 (SQLiteTracer: tabla 'trace') y .csv (CSVTracer).
# Todas las funciones retornan DataFrames con tiempos en segundos.
import os
import re
import sqlite3

import pandas as pd

# 'GPU1.Port->GPU2.Port' aparece en Location de fiber_transit y en IDs de reconfig
LINK_RE = re.compile(
    r"(?P<src>[\w\[\]\.\-]+?)\.Port\s*->\s*(?P<dst>[\w\[\]\.\-]+?)\.Port"
)

_NAT_RE = re.compile(r"(\d+)")

# 'GPU[1].SA[0].CU[0]' o 'GPU1.Port' -> prefijo de GPU
_GPU_RE = re.compile(r"^(GPU\[\d+\]|GPU\d+)")


def collapse_to_gpu(name):
    # Colapsa componentes internos al nivel de GPU: 'GPU[1].L2Cache[3]' -> 'GPU[1]'.
    # Componentes globales (Driver, MMU, OpticalSwitch) quedan igual. Sin esto,
    # las trazas mgpusim producen matrices de >100 componentes donde el tráfico
    # de la interconexión se pierde entre colas internas L1/ROB/TLB.
    m = _GPU_RE.match(str(name))
    return m.group(1) if m else str(name)


def is_gpu_node(name):
    # True si el nombre pertenece a una GPU ('GPU[1]', 'GPU2.L2Cache[3]'...).
    # Componentes globales del sistema (Driver, MMU, OpticalSwitch) -> False.
    return bool(_GPU_RE.match(str(name)))


def natural_key(name):
    # Clave de orden natural: GPU2 antes que GPU10.
    return [int(t) if t.isdigit() else t for t in _NAT_RE.split(str(name))]


def load_trace(path):
    # Normaliza la traza a columnas canónicas:
    # id, parent_id, kind, what, location, start_time, end_time (segundos, float)
    ext = os.path.splitext(path)[1].lower()
    if ext == ".sqlite3":
        conn = sqlite3.connect(path)
        df = pd.read_sql_query(
            "SELECT ID, ParentID, Kind, What, Location, StartTime, EndTime "
            "FROM trace",
            conn,
        )
        conn.close()
        df.columns = [
            "id", "parent_id", "kind", "what", "location",
            "start_time", "end_time",
        ]
    elif ext == ".csv":
        # CSVTracer usa otros nombres de columna que el SQLiteTracer
        df = pd.read_csv(path)
        df = df.rename(columns={
            "start_time_sec": "start_time",
            "end_time_sec": "end_time",
            "task_id": "id",
        })
    else:
        raise ValueError(f"Formato de traza no soportado: {path}")

    df["start_time"] = df["start_time"].astype(float)
    df["end_time"] = df["end_time"].astype(float)
    # Campos de texto sin NaN para poder filtrar/parsear sin sorpresas
    for col in ("id", "parent_id", "kind", "what", "location"):
        df[col] = df[col].fillna("").astype(str)
    return df


def parse_link(text):
    # 'GPU1.Port->GPU2.Port' -> ('GPU1', 'GPU2'), None si no hay enlace.
    m = LINK_RE.search(str(text))
    if m:
        return m.group("src"), m.group("dst")
    return None


def transit_events(df):
    # Tráfico real por enlace: tareas fiber_transit.
    # Vacío si la traza no registra fiber_transit.
    ft = df[df["kind"] == "fiber_transit"]
    rows = []
    for loc, t in zip(ft["location"], ft["start_time"]):
        # Location codifica el enlace, time = inicio del tránsito
        link = parse_link(loc)
        if link:
            rows.append((link[0], link[1], t))
    return pd.DataFrame(rows, columns=["src", "dst", "time"])


def demand_events(df):
    # Demanda de tráfico: join req_out (origen) <-> req_in (destino) vía parent_id.
    outs = df[df["kind"] == "req_out"][["id", "location", "start_time"]]
    ins = df[df["kind"] == "req_in"][["parent_id", "location"]]
    merged = ins.merge(
        outs, left_on="parent_id", right_on="id", suffixes=("_dst", "_src")
    )
    # time = inicio del req_out (momento en que el origen inyecta el mensaje),
    # independiente de cuándo se entregó. CLAVE para medir demanda pre-reconfig.
    return pd.DataFrame({
        "src": merged["location_src"],
        "dst": merged["location_dst"],
        "time": merged["start_time"],
    })


def _link_tasks(df, kind):
    sel = df[df["kind"] == kind].sort_values("start_time")
    rows = []
    for r in sel.itertuples(index=False):
        # El enlace puede venir codificado en el ID, en What o en Location,
        # src/dst quedan en None si el evento no lo codifica en ninguno
        link = parse_link(r.id) or parse_link(r.what) or parse_link(r.location)
        src, dst = link if link else (None, None)
        rows.append((float(r.start_time), float(r.end_time), src, dst))
    return pd.DataFrame(rows, columns=["t_start", "t_end", "src", "dst"])


def reconfig_events(df):
    # Eventos de reconfiguración óptica ordenados por tiempo:
    # DataFrame(t_start, t_end, src, dst)
    return _link_tasks(df, "reconfig")


def drain_events(df):
    # Eventos de drenaje óptico (desconexión de enlace), mismo formato.
    return _link_tasks(df, "drain")


def infer_reconfig_links(recs, trans, eps=2e-9):
    # Completa src/dst de reconfigs que no codifican el enlace en su ID.
    if trans.empty or recs.empty:
        return recs
    first_transit = trans.groupby(["src", "dst"])["time"].min()
    used = set()
    out = recs.copy()
    for idx, r in out.iterrows():
        if r["src"]:
            used.add((r["src"], r["dst"]))
            continue
        # Modelo de switch reactivo: los mensajes encolados fluyen apenas
        # termina la reconfiguración, así que el primer fiber_transit del
        # enlace recién conectado empieza ~ EndTime del reconfig.
        # Se asigna el enlace cuyo primer tránsito cae en [t_end - eps, t_end + eps].
        for (s, d), t in first_transit.items():
            if (s, d) not in used and abs(t - r["t_end"]) <= eps:
                out.at[idx, "src"] = s
                out.at[idx, "dst"] = d
                used.add((s, d))
                break
    return out


def detect_nodes(*event_frames):
    names = set()
    for ev in event_frames:
        if ev is None or ev.empty:
            continue
        for col in ("src", "dst"):
            if col in ev.columns:
                names.update(x for x in ev[col] if x and is_gpu_node(x))
    return sorted(names, key=natural_key)
