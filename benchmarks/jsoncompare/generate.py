#!/usr/bin/env python3
"""Deterministic nested order records; never buffers the whole fixture."""
import argparse, hashlib, json
from pathlib import Path

p = argparse.ArgumentParser()
p.add_argument('--bytes', type=int, default=500_000_000)
p.add_argument('--output', default='orders.json')
p.add_argument('--field-order', choices=['declaration', 'reverse'], default='declaration')
a = p.parse_args()

def field_order(value):
    if isinstance(value, dict):
        return {key: field_order(value[key]) for key in reversed(value)}
    if isinstance(value, list):
        return [field_order(item) for item in value]
    return value

path = Path(a.output)
expected = dict(records=0, ids=0, cents=0, units=0, text_bytes=0)
digest = hashlib.sha256()
size = 0
with path.open('wb', buffering=1024*1024) as f:
    def write(data):
        global size
        f.write(data); digest.update(data); size += len(data)
    write(b'[\n')
    i = 0
    while size < a.bytes - 2:
        i += 1
        description = ('Order notes: café, 東京, delivery 🚚. Handle with care; '
                       'customer requested gift wrapping. "Fragile"\nTracking\\warehouse. ') * 2
        items = [dict(sku=f'SKU-{(i+j)%10000:04d}', quantity=1+(i+j)%5,
                      unit_cents=199+(i*17+j*31)%20000) for j in range(3)]
        row = dict(id=i, customer=f'customer-{i%100000:05d}', active=i%3!=0,
                   total_cents=1000+(i*37)%100000, discount=None if i%4==0 else 'LOYALTY',
                   shipping=dict(city=['London','Córdoba','東京'][i%3], postal=f'{i%100000:05d}',
                                 latitude=round(-34.6+(i%1000)/10000,4), longitude=-58.38),
                   tags=['online','gift' if i%2 else 'standard','international'],
                   items=items, description=description)
        encoded = field_order(row) if a.field_order == 'reverse' else row
        data = json.dumps(encoded, ensure_ascii=False, separators=(',',':')).encode()
        if i > 1: write(b',\n')
        write(data)
        expected['records'] += 1; expected['ids'] += i
        expected['cents'] += row['total_cents']; expected['units'] += sum(x['quantity'] for x in items)
        expected['text_bytes'] += len(description.encode())
    write(b'\n]\n')
meta = dict(bytes=size, sha256=digest.hexdigest(), expected=expected, field_order=a.field_order)
path.with_suffix('.meta.json').write_text(json.dumps(meta, indent=2)+'\n')
print(json.dumps(meta))
