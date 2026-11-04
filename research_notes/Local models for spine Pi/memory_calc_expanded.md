# Memory calculations, expanded candidates (computed)
Same assumptions as memory_calc.md; sliding-window layers charged their window only (FP8).
```

## Laguna XS 2.1 33B-A3B: KV/token FP8 20.0 KiB; fixed/session 31 MB; session GB 0.70/1.37/2.72
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   FP8 (35.3)             -           -           -           -           10/5/2      31/16/8     
   Q6_K (29.0)            -           -           -           -           19/9/5      40/20/10    
   Q5_K_M (24.0)          -           -           5/2/1       1/0/0       26/13/6     47/24/12    
   Q4_K_M (20.3)          -           0/0/0       10/5/2      6/3/1       31/16/8     52/27/13    
   AWQ-INT4 (23.1)        -           -           6/3/1       2/1/0       27/14/7     48/24/12    

## Muse Glimmer 30B (dense): KV/token FP8 6.5 KiB; fixed/session 41 MB; session GB 0.26/0.48/0.91
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   FP8 (34.4)             -           -           -           -           31/17/9     88/48/25    
   UD-Q6_K_XL (26.3)      -           -           6/3/1       -           63/34/17    120/65/34   
   UD-Q5_K_M (19.2)       -           5/2/1       33/18/9     22/12/6     90/49/25    147/80/41   
   UD-Q4_K_XL (15.9)      -           18/9/5      46/25/13    34/18/9     103/56/29   160/86/45   
   INT4 (22.2)            -           -           22/12/6     10/5/3      78/42/22    135/73/38   

## North Mini Code 30B-A3B: KV/token FP8 13.0 KiB; fixed/session 151 MB; session GB 0.59/1.02/1.90
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   FP8 (32.0)             -           -           -           -           18/10/5     43/24/13    
   UD-Q6_K (25.5)         -           -           4/2/1       -           29/16/9     54/31/16    
   UD-Q5_K_M (22.9)       -           -           8/4/2       3/1/1       33/19/10    58/33/18    
   UD-Q4_K_XL (19.2)      -           2/1/0       14/8/4      9/5/3       39/22/12    64/37/20    
   AWQ-INT4 (18.5)        -           3/2/1       16/9/4      11/6/3      41/23/12    66/38/20    

## Mellum2.1 12B-A2.5B: KV/token FP8 7.0 KiB; fixed/session 22 MB; session GB 0.26/0.49/0.96
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   FP8 (12.6)             2/1/0       30/16/8     59/31/15    47/25/12    116/61/31   174/91/46   
   Q6_K (10.9)            9/4/2       37/19/10    66/34/17    54/28/14    123/64/33   181/94/48   
   Q5_K_M (10.0)          12/6/3      41/21/11    69/36/18    58/30/15    127/66/33   184/96/49   
   Q4_K_M (8.1)           20/10/5     48/25/13    77/40/20    65/34/17    134/70/35   191/100/51  
   AWQ-INT4 (7.7)         21/11/5     50/26/13    78/41/21    67/35/17    136/71/36   193/101/51  

## Nemotron 3.5 Bolt 74B-A9B: KV/token FP8 4.0 KiB; fixed/session 168 MB; session GB 0.30/0.44/0.70
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   IQ2 (NVIDIA 24GB) (24.1)-           -           12/8/5      2/1/1       61/42/26    110/76/47   
   UD-Q5_K_XL (58.9)      -           -           -           -           -           -           
   UD-Q4_K_XL (50.2)      -           -           -           -           -           23/16/10    

## KAT-Coder-V2.5-Dev (35B-A3B): KV/token FP8 10.0 KiB; fixed/session 63 MB; session GB 0.40/0.73/1.41
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   Q6_K (30.1)            -           -           -           -           31/17/8     68/37/19    
   Q5_K_M (25.0)          -           -           7/3/2       -           44/24/12    81/44/23    
   Q4_K_M (21.4)          -           -           16/8/4      8/4/2       53/28/15    90/49/25    
   IQ4_XS (18.8)          -           4/2/1       22/12/6     15/8/4      59/32/16    96/52/27    
   AWQ-INT4 (24.4)        -           -           8/4/2       1/0/0       45/24/12    82/44/23    

## Ornith-1.5-35B-A3B: KV/token FP8 10.0 KiB; fixed/session 63 MB; session GB 0.40/0.73/1.41
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   Q6_K (29.2)            -           -           -           -           33/18/9     70/38/20    
   Q5_K_M (25.4)          -           -           6/3/1       -           43/23/12    80/43/22    
   Q4_K_M (21.7)          -           -           15/8/4      8/4/2       52/28/14    89/48/25    

## Seed-OSS-36B (dense): KV/token FP8 128.0 KiB; fixed/session 0 MB; session GB 4.29/8.59/17.18
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   Q6_K (29.7)            -           -           -           -           3/1/0       6/3/1       
   Q5_K_M (25.6)          -           -           0/0/0       -           3/1/0       7/3/1       
   UD-Q4_K_XL (22.0)      -           -           1/0/0       0/0/0       4/2/1       8/4/2       

## MiniMax-M2.5-REAP-139B-A10B: KV/token FP8 124.0 KiB; fixed/session 0 MB; session GB 4.16/8.32/16.64
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   IQ3_XXS (53.6)         -           -           -           -           -           0/0/0       
   Q2_K (50.8)            -           -           -           -           -           1/0/0       

## GLM-4.5-Air-REAP-82B-A12B: KV/token FP8 92.0 KiB; fixed/session 0 MB; session GB 3.09/6.17/12.35
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   Q4_K_M (56.6)          -           -           -           -           -           0/0/0       
   IQ4_XS (47.0)          -           -           -           -           -           3/1/0       
   Q3_K_M (42.9)          -           -           -           -           -           4/2/1       

## Qwen3-Coder-REAP-25B-A3B: KV/token FP8 48.0 KiB; fixed/session 0 MB; session GB 1.61/3.22/6.44
   quant                  16 GB       24 GB       1x32 GB     2x16 TP     48 GB       64 GB       
   Q6_K (20.4)            -           0/0/0       4/2/1       2/1/0       13/6/3      22/11/5     
   Q4_K_M (15.2)          -           3/1/0       7/3/1       6/3/1       17/8/4      26/13/6     
```
```python
G=1e9
# kv: elems/token across full-attention layers (FP8 ~1 B/elem). state: fixed bytes/session (SWA window cache at 1 B/elem, recurrent state fp32).
M = {
 "Laguna XS 2.1 33B-A3B": dict(kv=10*8*128*2, state=30*512*8*128*2,
     w={"FP8":35.32,"Q6_K":29.04,"Q5_K_M":24.01,"Q4_K_M":20.27,"AWQ-INT4":23.07}),
 "Muse Glimmer 30B (dense)": dict(kv=13*2*128*2, state=39*2048*2*128*2,
     w={"FP8":34.39,"UD-Q6_K_XL":26.27,"UD-Q5_K_M":19.19,"UD-Q4_K_XL":15.88,"INT4":22.20}),
 "North Mini Code 30B-A3B": dict(kv=13*4*128*2, state=36*4096*4*128*2,
     w={"FP8":32.03,"UD-Q6_K":25.51,"UD-Q5_K_M":22.95,"UD-Q4_K_XL":19.25,"AWQ-INT4":18.47}),
 "Mellum2.1 12B-A2.5B": dict(kv=7*4*128*2, state=21*1024*4*128*2,
     w={"FP8":12.62,"Q6_K":10.88,"Q5_K_M":9.97,"Q4_K_M":8.07,"AWQ-INT4":7.68}),
 "Nemotron 3.5 Bolt 74B-A9B": dict(kv=8*2*128*2, state=40*128*64*128*4,
     w={"IQ2 (NVIDIA 24GB)":24.08,"UD-Q5_K_XL":58.91,"UD-Q4_K_XL":50.22}),
 "KAT-Coder-V2.5-Dev (35B-A3B)": dict(kv=10*2*256*2, state=30*32*128*128*4,
     w={"Q6_K":30.05,"Q5_K_M":25.02,"Q4_K_M":21.39,"IQ4_XS":18.81,"AWQ-INT4":24.43}),
 "Ornith-1.5-35B-A3B": dict(kv=10*2*256*2, state=30*32*128*128*4,
     w={"Q6_K":29.21,"Q5_K_M":25.35,"Q4_K_M":21.71}),
 "Seed-OSS-36B (dense)": dict(kv=64*8*128*2, state=0,
     w={"Q6_K":29.67,"Q5_K_M":25.59,"UD-Q4_K_XL":22.03}),
 "MiniMax-M2.5-REAP-139B-A10B": dict(kv=62*8*128*2, state=0,
     w={"IQ3_XXS":53.61,"Q2_K":50.82}),
 "GLM-4.5-Air-REAP-82B-A12B": dict(kv=46*8*128*2, state=0,
     w={"Q4_K_M":56.57,"IQ4_XS":46.99,"Q3_K_M":42.88}),
 "Qwen3-Coder-REAP-25B-A3B": dict(kv=48*4*128*2, state=0,
     w={"Q6_K":20.45,"Q4_K_M":15.19}),
}
CTX=[32768,65536,131072]
TIERS=[("16 GB",16,1),("24 GB",24,1),("1x32 GB",32,1),("2x16 TP",32,2),("48 GB",48,1),("64 GB",64,1)]
def usable(t,g): return t*0.92-g*1.5-(g*0.75 if g>1 else 0)
for n,m in M.items():
    s={c:(m["kv"]*c+m["state"])/G for c in CTX}
    print(f"\n## {n}: KV/token FP8 {m['kv']/1024:.1f} KiB; fixed/session {m['state']/1e6:.0f} MB; session GB {s[32768]:.2f}/{s[65536]:.2f}/{s[131072]:.2f}")
    print("   quant".ljust(26)+"".join(t.ljust(12) for t,_,_ in TIERS))
    for q,w in m["w"].items():
        cells=[]
        for t,tot,g in TIERS:
            f=usable(tot,g)-w
            cells.append(("-" if f<=0 else "/".join(str(int(f//s[c])) for c in CTX)).ljust(12))
        print("   "+f"{q} ({w:.1f})".ljust(23)+"".join(cells))
```
