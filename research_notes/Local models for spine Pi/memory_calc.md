# Memory calculations (computed)
Assumptions: 92% of VRAM usable; 1.5 GB runtime overhead per GPU; +0.75 GB per GPU for tensor parallelism; KV at FP8/Q8_0 ~1 byte/element (Q8_0 is 6% more); recurrent DeltaNet/Mamba state fp32. Cells = concurrent sessions at 32K/64K/128K; "-" = weights do not fit.
```

## Qwen3-Coder-Next 80B-A3B: KV/token FP8 = 12.0 KiB; fixed state/session = 77 MB
   session GB @32K/64K/128K: 0.48 / 0.88 / 1.69
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   FP8 (80.4GB)       -                   -                   -                   -                   -                   -                   
   Q6_K (65.6GB)      -                   -                   -                   -                   -                   -                   
   Q5_K_M (56.9GB)    -                   -                   -                   -                   -                   1/0/0               
   UD-Q4_K_XL (49.6GB)-                   -                   -                   -                   -                   16/8/4              
   AWQ-INT4 (48.2GB)  -                   -                   -                   -                   -                   19/10/5             

## Qwen3-Coder-30B-A3B: KV/token FP8 = 48.0 KiB; fixed state/session = 0 MB
   session GB @32K/64K/128K: 1.61 / 3.22 / 6.44
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   FP8 (31.2GB)       -                   -                   -                   -                   7/3/1               16/8/4              
   Q6_K (25.1GB)      -                   1/0/0               -                   -                   10/5/2              20/10/5             
   Q5_K_M (21.7GB)    -                   3/1/0               -                   1/0/0               12/6/3              22/11/5             
   UD-Q4_K_XL (17.7GB)-                   6/3/1               1/0/0               4/2/1               15/7/3              24/12/6             
   AWQ-INT4 (16.8GB)  -                   6/3/1               2/1/0               5/2/1               16/8/4              25/12/6             

## Qwen3.8-27B (dense): KV/token FP8 = 32.0 KiB; fixed state/session = 154 MB
   session GB @32K/64K/128K: 1.23 / 2.30 / 4.45
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   FP8 (30.9GB)       -                   -                   -                   -                   9/5/2               21/11/5             
   Q6_K (UD) (22.0GB) -                   4/2/1               -                   2/1/0               16/8/4              28/15/7             
   Q5_K_M (UD) (19.8GB)-                   6/3/1               0/0/0               4/2/1               18/9/5              30/16/8             
   UD-Q4_K_XL (17.6GB)-                   8/4/2               2/1/0               6/3/1               20/10/5             32/17/8             
   AWQ-INT4 (19.4GB)  -                   6/3/1               0/0/0               4/2/1               18/10/5             30/16/8             

## Qwen3.6-35B-A3B: KV/token FP8 = 10.0 KiB; fixed state/session = 64 MB
   session GB @32K/64K/128K: 0.40 / 0.74 / 1.41
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   FP8 (37.5GB)       -                   -                   -                   -                   13/7/3              49/27/14            
   Q6_K (UD) (29.3GB) -                   -                   -                   -                   33/18/9             70/38/19            
   Q5_K_M (UD) (26.5GB)-                   3/2/1               -                   -                   40/22/11            77/42/21            
   UD-Q4_K_XL (22.4GB)-                   13/7/3              -                   6/3/1               50/27/14            87/47/24            
   AWQ-INT4 (25.5GB)  -                   6/3/1               -                   -                   43/23/12            79/43/22            

## Qwen3-14B (dense): KV/token FP8 = 80.0 KiB; fixed state/session = 0 MB
   session GB @32K/64K/128K: 2.68 / 5.37 / 10.74
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   FP8 (16.3GB)       -                   4/2/1               1/0/0               3/1/0               9/4/2               15/7/3              
   Q6_K (12.1GB)      0/0/0               5/2/1               3/1/0               4/2/1               11/5/2              16/8/4              
   Q5_K_M (10.5GB)    1/0/0               6/3/1               3/1/0               5/2/1               11/5/2              17/8/4              
   UD-Q4_K_XL (9.2GB) 1/0/0               6/3/1               4/2/1               5/2/1               12/6/3              17/8/4              
   AWQ-INT4 (10.0GB)  1/0/0               6/3/1               3/1/0               5/2/1               12/6/3              17/8/4              

## Devstral Small 2 24B (dense): KV/token FP8 = 80.0 KiB; fixed state/session = 0 MB
   session GB @32K/64K/128K: 2.68 / 5.37 / 10.74
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   FP8 (25.8GB)       -                   0/0/0               -                   -                   6/3/1               11/5/2              
   Q6_K (19.4GB)      -                   3/1/0               0/0/0               2/1/0               8/4/2               14/7/3              
   Q5_K_M (16.8GB)    -                   4/2/1               1/0/0               3/1/0               9/4/2               15/7/3              
   UD-Q4_K_XL (14.5GB)-                   5/2/1               2/1/0               3/1/0               10/5/2              15/7/3              
   AWQ-INT4 (16.1GB)  -                   4/2/1               1/0/0               3/1/0               9/4/2               15/7/3              

## gpt-oss-20b: KV/token FP8 = 12.0 KiB; fixed state/session = 2 MB
   session GB @32K/64K/128K: 0.40 / 0.81 / 1.61
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   MXFP4 (native) (13.8GB)-                   35/17/8             16/8/4              27/13/6             71/35/17            107/54/27           
   Q6_K (12.0GB)      2/1/0               39/19/9             21/10/5             31/15/8             75/37/18            112/56/28           
   Q5_K_M (11.7GB)    3/1/0               40/20/10            21/10/5             32/16/8             76/38/19            112/56/28           
   UD-Q4_K_XL (11.9GB)3/1/0               39/19/9             21/10/5             32/16/8             76/38/19            112/56/28           

## Nemotron 3.5 Lightning 30B-A3B: KV/token FP8 = 3.0 KiB; fixed state/session = 48 MB
   session GB @32K/64K/128K: 0.15 / 0.25 / 0.45
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   FP8 (32.3GB)       -                   -                   -                   -                   69/41/22            168/100/55          
   Q6_K (34.3GB)      -                   -                   -                   -                   56/33/18            154/92/51           
   Q5_K_M (27.0GB)    -                   6/3/2               -                   -                   105/62/34           204/121/67          
   UD-Q4_K_XL (25.5GB)-                   16/9/5              -                   -                   115/68/38           214/127/70          
   NVFP4 (20.4GB)     -                   50/30/16            1/0/0               30/18/10            149/89/49           248/148/81          

## GLM-4.7-Flash 31B-A3B (MLA): KV/token FP8 = 26.4 KiB; fixed state/session = 0 MB
   session GB @32K/64K/128K: 0.89 / 1.77 / 3.55
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   Q6_K (24.7GB)      -                   3/1/0               -                   0/0/0               20/10/5             36/18/9             
   Q5_K_M (21.4GB)    -                   7/3/1               -                   3/1/0               23/11/5             40/20/10            
   UD-Q4_K_XL (17.5GB)-                   11/5/2              3/1/0               8/4/2               28/14/7             44/22/11            

## Granite 4.2 30B (dense): KV/token FP8 = 128.0 KiB; fixed state/session = 0 MB
   session GB @32K/64K/128K: 4.29 / 8.59 / 17.18
   quant              16 GB               1x32 GB             24 GB               32 GB (2x16 TP)     48 GB               64 GB               
   FP8 (30.1GB)       -                   -                   -                   -                   2/1/0               6/3/1               
   Q6_K (24.0GB)      -                   0/0/0               -                   0/0/0               4/2/1               7/3/1               
   Q5_K_M (20.8GB)    -                   1/0/0               -                   0/0/0               5/2/1               8/4/2               

usable GB per tier: [('16 GB', 13.22), ('1x32 GB', 27.94), ('24 GB', 20.58), ('32 GB (2x16 TP)', 24.94), ('48 GB', 42.66), ('64 GB', 57.38)]
```

Script:
```python
import json
G=1e9
# kv elems per token (all full-attention layers): layers*kv_heads*head_dim*2 ; MLA: layers*(kv_lora+rope)
M = {
 "Qwen3-Coder-Next 80B-A3B": dict(kv=12*2*256*2, state=36*32*128*128*4 + 36*(2*16*128+32*128)*3*2,
     w={"FP8":80.38,"Q6_K":65.61,"Q5_K_M":56.85,"UD-Q4_K_XL":49.61,"AWQ-INT4":48.25}),
 "Qwen3-Coder-30B-A3B": dict(kv=48*4*128*2, state=0,
     w={"FP8":31.18,"Q6_K":25.09,"Q5_K_M":21.73,"UD-Q4_K_XL":17.67,"AWQ-INT4":16.81}),
 "Qwen3.8-27B (dense)": dict(kv=16*4*256*2, state=48*48*128*128*4 + 48*(2*16*128+48*128)*3*2,
     w={"FP8":30.87,"Q6_K (UD)":21.98,"Q5_K_M (UD)":19.77,"UD-Q4_K_XL":17.56,"AWQ-INT4":19.45}),
 "Qwen3.6-35B-A3B": dict(kv=10*2*256*2, state=30*32*128*128*4 + 30*(2*16*128+32*128)*3*2,
     w={"FP8":37.46,"Q6_K (UD)":29.31,"Q5_K_M (UD)":26.46,"UD-Q4_K_XL":22.36,"AWQ-INT4":25.46}),
 "Qwen3-14B (dense)": dict(kv=40*8*128*2, state=0,
     w={"FP8":16.33,"Q6_K":12.12,"Q5_K_M":10.51,"UD-Q4_K_XL":9.16,"AWQ-INT4":9.98}),
 "Devstral Small 2 24B (dense)": dict(kv=40*8*128*2, state=0,
     w={"FP8":25.79,"Q6_K":19.35,"Q5_K_M":16.76,"UD-Q4_K_XL":14.51,"AWQ-INT4":16.07}),
 "gpt-oss-20b": dict(kv=12*8*64*2, state=12*128*8*64*2,  # 12 SWA layers x128-token window
     w={"MXFP4 (native)":13.76,"Q6_K":12.04,"Q5_K_M":11.72,"UD-Q4_K_XL":11.87}),
 "Nemotron 3.5 Lightning 30B-A3B": dict(kv=6*2*128*2, state=23*64*64*128*4,
     w={"FP8":32.29,"Q6_K":34.31,"Q5_K_M":26.96,"UD-Q4_K_XL":25.51,"NVFP4":20.43}),
 "GLM-4.7-Flash 31B-A3B (MLA)": dict(kv=47*(512+64), state=0,
     w={"Q6_K":24.69,"Q5_K_M":21.41,"UD-Q4_K_XL":17.52}),
 "Granite 4.2 30B (dense)": dict(kv=64*8*128*2, state=0,
     w={"FP8":30.11,"Q6_K":24.02,"Q5_K_M":20.78}),
}
CTX=[32768,65536,131072]
# tiers: name, total GB, gpus
TIERS=[("16 GB",16,1),("1x32 GB",32,1),("24 GB",24,1),("32 GB (2x16 TP)",32,2),("48 GB",48,1),("64 GB",64,1)]
UTIL=0.92; OVH=1.5; TPOVH=0.75  # GB per GPU runtime overhead; extra per GPU for TP (NCCL, duplicated buffers)
def usable(total,g): return total*UTIL - g*OVH - (g*TPOVH if g>1 else 0)
out={}
for name,m in M.items():
    kvb=m["kv"]*1.0  # FP8 / ~Q8_0 bytes per token (Q8_0 is 1.0625x)
    row={"kv_bytes_per_token_fp8":kvb,"state_MB":m["state"]/1e6,
         "session_GB":{c:(kvb*c+m["state"])/G for c in CTX},"fits":{}}
    for q,w in m["w"].items():
        for t,tot,g in TIERS:
            free=usable(tot,g)-w
            row["fits"][(q,t)]={c:(int(free//row["session_GB"][c]) if free>0 else None) for c in CTX}
    out[name]=row
for name,r in out.items():
    print(f"\n## {name}: KV/token FP8 = {r['kv_bytes_per_token_fp8']/1024:.1f} KiB; fixed state/session = {r['state_MB']:.0f} MB")
    print("   session GB @32K/64K/128K:", " / ".join(f"{r['session_GB'][c]:.2f}" for c in CTX))
    qs=sorted({q for q,_ in r["fits"]},key=list(M[name]["w"]).index)
    print("   quant".ljust(22)+"".join(t.ljust(20) for t,_,_ in TIERS))
    for q in qs:
        cells=[]
        for t,_,_ in TIERS:
            v=r["fits"][(q,t)]
            cells.append(("-" if v[32768] is None else f"{v[32768]}/{v[65536]}/{v[131072]}").ljust(20))
        print("   "+f"{q} ({M[name]['w'][q]:.1f}GB)".ljust(19)+"".join(cells))
print("\nusable GB per tier:",[ (t,round(usable(tot,g),2)) for t,tot,g in TIERS])
```
