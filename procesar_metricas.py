import os
import sys
import sqlite3
import csv
import glob
import re

# -- MÉTRICAS EN .SQLITE3 --
METRICS_TO_SUM = [
    'cu_inst_count', 'read_size', 'write_size', 
    'incoming_trans_count', 'outgoing_trans_count', 
    'read_trans_count', 'write_trans_count'
]
METRICS_TO_AVG = [
    'read_avg_latency', 'req_average_latency', 'write_avg_latency'
]

# -- RAW DATA --
def process_sqlite(db_path, num_gpus, out_dir):    
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()
    
    gpu_data = {}
    
    try:
        cursor.execute("SELECT Location, What, Value FROM mgpusim_metrics")
        rows = cursor.fetchall()
    except sqlite3.OperationalError:
        print(f"ADVERTENCIA: La tabla mgpusim_metrics no existe en {db_path}")
        conn.close()
        return None

    # Iterar en rawdata y agrupar por GPU
    for location, what, value in rows:
        # IGNORAR NULOS
        if value is None:
            continue
            
        # Extraer el ID de la GPU
        match = re.search(r'GPU\[(\d+)\]', str(location))
        if not match:
            continue 
        
        gpu_id = f"GPU[{match.group(1)}]"
        value = float(value)
        
        if gpu_id not in gpu_data:
            gpu_data[gpu_id] = {m: 0.0 for m in METRICS_TO_SUM}
            gpu_data[gpu_id].update({m: [] for m in METRICS_TO_AVG})
            gpu_data[gpu_id]['kernel_time'] = 0.0

        if what in METRICS_TO_SUM:
            gpu_data[gpu_id][what] += value
        elif what in METRICS_TO_AVG:
            gpu_data[gpu_id][what].append(value)
        elif what == 'kernel_time':
            if value > gpu_data[gpu_id]['kernel_time']:
                gpu_data[gpu_id]['kernel_time'] = value

    conn.close()

    if not gpu_data:
        print(f"  -> ADVERTENCIA: No data de GPU en {os.path.basename(db_path)}")
        return None

    raw_csv_path = os.path.join(out_dir, f"raw_{num_gpus}gpus.csv")
    headers = ['GPU_ID', 'kernel_time'] + METRICS_TO_SUM + METRICS_TO_AVG
    
    run_summary = {
        'num_gpus': num_gpus,
        'max_kernel_time': 0.0,
        'total_cu_inst_count': 0.0,
        'total_read_size': 0.0,
        'total_write_size': 0.0,
        'total_incoming_trans_count': 0.0,
        'total_outgoing_trans_count': 0.0,
        'total_read_trans_count': 0.0,
        'total_write_trans_count': 0.0,
        'avg_read_latency': [],
        'avg_req_latency': [],
        'avg_write_latency': []
    }

    with open(raw_csv_path, 'w', newline='', encoding='utf-8') as f:
        writer = csv.writer(f, delimiter=',')
        writer.writerow(headers)
        
        for gpu_id, metrics in sorted(gpu_data.items()):
            row = [gpu_id, metrics['kernel_time']]
            run_summary['max_kernel_time'] = max(run_summary['max_kernel_time'], metrics['kernel_time'])
            
            for m in METRICS_TO_SUM:
                row.append(metrics[m])
                if f"total_{m}" in run_summary:
                    run_summary[f"total_{m}"] += metrics[m]
            
            for m in METRICS_TO_AVG:
                avg_val = sum(metrics[m])/len(metrics[m]) if len(metrics[m]) > 0 else 0.0
                row.append(avg_val)
                if len(metrics[m]) > 0:
                    global_key = m.replace('average', 'avg')
                    if f"avg_{global_key.replace('_avg', '')}" in run_summary:
                        run_summary[f"avg_{global_key.replace('_avg', '')}"].extend(metrics[m])

            writer.writerow(row)
            
    for key in ['avg_read_latency', 'avg_req_latency', 'avg_write_latency']:
        arr = run_summary[key]
        run_summary[key] = sum(arr)/len(arr) if len(arr) > 0 else 0.0
        
    sim_time_sec = run_summary['max_kernel_time']
    total_bytes = run_summary['total_read_size'] + run_summary['total_write_size']
    total_inst = run_summary['total_cu_inst_count']
    
    run_summary['mem_throughput_gbps'] = (total_bytes / (1024**3)) / sim_time_sec if sim_time_sec > 0 else 0.0
    run_summary['compute_throughput_ginstps'] = (total_inst / (10**9)) / sim_time_sec if sim_time_sec > 0 else 0.0
    run_summary['max_kernel_time_us'] = sim_time_sec * 1_000_000 

    return run_summary


# -- MAIN Y MÉTRICAS GENERALES --

def main():
    out_dir = sys.argv[1]
    sqlite_files = glob.glob(os.path.join(out_dir, "metrics_*gpus.sqlite3"))
    
    if not sqlite_files:
        print(f"No está 'metrics_<N>gpus.sqlite3' en {out_dir}")
        return

    summaries = []
    
    for db_path in sqlite_files:
        match = re.search(r'metrics_(\d+)gpus', os.path.basename(db_path))
        if match:
            num_gpus = int(match.group(1))
            summary = process_sqlite(db_path, num_gpus, out_dir)
            if summary:
                summaries.append(summary)
                
    if summaries:
        summaries.sort(key=lambda x: x['num_gpus'])
        
        headers = [
            'Num_GPUs', 'Max_Kernel_Time_us', 'Total_CU_Inst_Count', 
            'Total_Read_Size_Bytes', 'Total_Write_Size_Bytes',
            'Mem_Throughput_GBps', 'Compute_Throughput_GInstps',
            'Total_Incoming_Trans', 'Total_Outgoing_Trans', 
            'Total_Read_Trans', 'Total_Write_Trans',
            'Global_Avg_Read_Latency', 'Global_Avg_Req_Latency', 'Global_Avg_Write_Latency'
        ]
        
        summary_path = os.path.join(out_dir, "resumen_topologia.csv")
        with open(summary_path, 'w', newline='', encoding='utf-8') as f:
            writer = csv.writer(f, delimiter=';') 
            writer.writerow(headers)
            
            for s in summaries:
                row = [
                    s['num_gpus'], 
                    f"{s['max_kernel_time_us']:.2f}".replace('.', ','),
                    int(s['total_cu_inst_count']),
                    int(s['total_read_size']),
                    int(s['total_write_size']),
                    f"{s['mem_throughput_gbps']:.6f}".replace('.', ','),
                    f"{s['compute_throughput_ginstps']:.6f}".replace('.', ','),
                    int(s['total_incoming_trans_count']),
                    int(s['total_outgoing_trans_count']),
                    int(s['total_read_trans_count']),
                    int(s['total_write_trans_count']),
                    f"{s['avg_read_latency']:.6f}".replace('.', ','),
                    f"{s['avg_req_latency']:.6f}".replace('.', ','),
                    f"{s['avg_write_latency']:.6f}".replace('.', ',')
                ]
                writer.writerow(row)
                
        print(f"  -> Resumen de topología creado en: {summary_path}")

if __name__ == "__main__":
    main()