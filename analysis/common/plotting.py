# Heatmaps estándar del pipeline: un solo estilo para todas las matrices.
import matplotlib

# Backend sin display: solo escribimos PNGs
matplotlib.use("Agg")

# Usamos noqa: E402 para intencionalmente importar matplotlib y seaborn
# después de configurar el backend (aunque deberían estar los imports al inicio)
import matplotlib.pyplot as plt # noqa: E402
import seaborn as sns # noqa: E402
from . import units # noqa: E402


def save_matrix_csv(M, csv_path):
    # CSV raw: filas = origen, columnas = destino, sin escalar
    M.to_csv(csv_path, index_label="origen")


def save_heatmap(M, png_path, title, cbar_label, vmax=None):
    # Tamaño de figura crece con el número de nodos, con techo: trazas mgpusim
    # tienen >100 componentes y una figura proporcional revienta la memoria
    # del render (~1 GB de píxeles por PNG)
    n = max(len(M.index), 2)
    side = min(max(5.0, 0.9 * n + 2.5), 16.0)
    fig, ax = plt.subplots(figsize=(side + 1.5, side))
    sns.heatmap(
        M, ax=ax, cmap="YlOrRd", vmin=0.0, vmax=vmax,
        # Valores anotados solo en matrices chicas; en grandes son ilegibles
        annot=(n <= 16), fmt=".3g", square=True,
        linewidths=0.5, linecolor="white",
        cbar_kws={"label": cbar_label},
    )
    ax.set_xlabel("Destino")
    ax.set_ylabel("Origen")
    ax.set_title(title)
    fig.tight_layout()
    fig.savefig(png_path, dpi=150)
    plt.close(fig)


def save_matrix_and_heatmap(M, base_path, title, in_bytes=False, vmax=None):
    # Guarda base_path.csv (valores crudos) + base_path.png (heatmap)
    save_matrix_csv(M, base_path + ".csv")
    disp = M
    label = "mensajes"
    if in_bytes:
        # El CSV conserva bytes crudos, el heatmap se re-escala a la
        # unidad legible (KB/MB/GB) según el máximo de la matriz
        factor, unit = units.pick_bytes_unit(float(M.to_numpy().max()))
        disp = M * factor
        label = unit
        if vmax is not None:
            vmax = vmax * factor
    save_heatmap(disp, base_path + ".png", title, f"Tráfico [{label}]", vmax=vmax)
