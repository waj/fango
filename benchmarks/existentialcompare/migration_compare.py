#!/usr/bin/env python3
"""Compare generated hidden-state library workers with explicit-state workers.

Use Nix python3. Preparation validates checksums, repeatability, retained iterator
positions, allocation slopes, race and checkptr. Timings require an idle host.
Both snapshots use the same compiler; only the public Stream/Iterator state
parameter and the corresponding source annotations differ.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

from compare import summarize


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--prepare-only', action='store_true')
    parser.add_argument('--idle', action='store_true')
    parser.add_argument('--resume', action='store_true')
    args = parser.parse_args()
    if not args.prepare_only and not args.idle:
        parser.error('timings require --idle')
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=args.resume)
    here = Path(__file__).resolve().parent
    repo = here.parent.parent
    env = dict(os.environ, GOMAXPROCS='1', GOGC='100')
    env.setdefault('GOCACHE', str(out/'gocache'))
    env.setdefault('GOMODCACHE', str(out/'gomodcache'))

    def command(argv, cwd=repo, extra=None):
        result = subprocess.run(argv,cwd=cwd,env={**env,**(extra or {})},text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
        with (out/'commands.log').open('a') as f:
            f.write(repr(argv)+'\n'+result.stdout+'\n')
        if result.returncode:
            raise RuntimeError(result.stdout)
        return result.stdout

    variants = ['explicit','hidden']
    if not args.resume:
        names = command(['git','ls-files','-z','--cached','--others','--exclude-standard']).split('\0')
        allocations={}
        for variant in variants:
            snapshot = out/variant/'repo'
            for name in sorted(set(names)-{''}):
                source = repo/name
                if not source.is_file(): continue
                target=snapshot/name
                target.parent.mkdir(parents=True,exist_ok=True)
                target.write_bytes(source.read_bytes())
            fixtures=snapshot/'benchmarks/existentialcompare'
            if variant=='explicit':
                for module in ['Stream','Iterator']:
                    shutil.copyfile(fixtures/f'explicit_{module}.fango.txt',snapshot/f'stdlib/{module}.fango')
                shutil.copyfile(fixtures/'explicit_workload.fango.txt',fixtures/'workload.fango')
            hashes={str(p.relative_to(snapshot)):hashlib.sha256(p.read_bytes()).hexdigest() for p in snapshot.rglob('*') if p.is_file()}
            (out/variant/'source-hashes.json').write_text(json.dumps(hashes,indent=2)+'\n')
            cli=out/variant/'fango'; project=out/variant/'generated'
            command(['go','build','-o',str(cli),'./cmd/fango'],snapshot)
            command([str(cli),'build','--emit-go','-o',str(project),str(fixtures/'workload.fango')],snapshot,{'FANGO_ROOT':str(snapshot)})
            test=(fixtures/'migration_test.go.txt').read_text()
            test=test.replace('STREAMTYPE','m_Stream.T_Stream_dot_Stream[S, int64]' if variant=='explicit' else 'm_Stream.T_Stream_dot__storage_Stream[int64, S]')
            prefix='V_' if variant=='explicit' else 'V__storage_'
            for marker,name in [('SUM','sum'),('CURSOR','cursorSum'),('SCALAR','scalar'),('PAIR','pair'),('LINKED','linked'),('MAPPED','mapped'),('REJECTED','rejected'),('TAKEN','taken'),('ZIPPED','zipped')]:
                test=test.replace(marker,prefix+name)
            (project/'entries/workload/migration_test.go').write_text(test)
            checked=command(['go','test','-v','-race','-gcflags=all=-d=checkptr=2','-count=1','./entries/workload'],project)
            allocations[variant]={f'{case}/{n}/{alt}':int(count) for case,n,alt,count in re.findall(r'ALLOC (\w+) (\d+) (true|false) (\d+)',checked)}
            command(['go','test','-c','-o',str(out/variant/'bench.test'),'./entries/workload'],project)
        (out/'allocations.json').write_text(json.dumps(allocations,indent=2)+'\n')
        regressions={case:(count,allocations['explicit'][case]) for case,count in allocations['hidden'].items() if count>allocations['explicit'][case]}
        if regressions: raise RuntimeError(f'allocation regressions: {regressions}')
        metadata=dict(go=command(['go','version']).strip(),hardware=command(['uname','-a']).strip(),GOMAXPROCS=1,GOGC=100,sample_ms=100,
                      binaries={v:hashlib.sha256((out/v/'bench.test').read_bytes()).hexdigest() for v in variants})
        (out/'metadata.json').write_text(json.dumps(metadata,indent=2)+'\n')
        print('Both compiler snapshots passed checksums, repeatability, iterator aliases, allocation slopes, race and checkptr.',flush=True)
    if args.prepare_only: return
    metadata=json.loads((out/'metadata.json').read_text())
    for variant in variants:
        actual=hashlib.sha256((out/variant/'bench.test').read_bytes()).hexdigest()
        if actual!=metadata['binaries'][variant]: raise RuntimeError('prepared benchmark binary changed')
    metadata['idle_asserted']=True
    (out/'metadata.json').write_text(json.dumps(metadata,indent=2)+'\n')
    samples_path=out/'samples.json'
    samples=json.loads(samples_path.read_text()) if args.resume and samples_path.exists() else {}

    def report():
        if len(samples)!=60: raise RuntimeError(f'expected 60 cases, got {len(samples)}')
        results=[dict(case=case,samples=len(rows)//2,**summarize(rows,'hidden')) for case,rows in samples.items()]
        for result in results:
            if result['samples']<20: result['verdict']='inconclusive'
            if result['hidden']['Allocs'] > result['explicit']['Allocs']:
                result['verdict']='fail'
                result['allocation_regression']=True
        (out/'summary.json').write_text(json.dumps(results,indent=2)+'\n')
        counts={v:sum(r['verdict']==v for r in results) for v in ['pass','fail','inconclusive']}
        print(json.dumps(dict(min_samples=min(r['samples'] for r in results),max_samples=max(r['samples'] for r in results),**counts)),flush=True)
        return results

    results=report() if samples else []
    active=[r['case'] for r in results if r['verdict']=='inconclusive' and r['samples']<60]
    start=min((len(samples[case])//2 for case in active),default=0 if not samples else 60)
    for sample in range(start,60):
        targets=[None] if sample<20 else [case for case in active if len(samples[case])//2==sample]
        if not targets: continue
        for variant in variants if sample%2==0 else variants[::-1]:
            for case in targets:
                pattern='BenchmarkMigration' if case is None else '^BenchmarkMigration$/'+('/'.join('^'+re.escape(part)+'$' for part in case.split('/')))
                output=command([str(out/variant/'bench.test'),'-test.run=^$','-test.bench='+pattern,'-test.benchtime=100ms','-test.count=1'],out/variant/'generated')
                observed=0
                for name,reps,ns,size,allocs in re.findall(r'BenchmarkMigration/(\S+)\s+(\d+)\s+([\d.]+) ns/op\s+(\d+) B/op\s+(\d+) allocs/op',output):
                    if case is not None and name!=case: raise RuntimeError('unexpected benchmark '+name)
                    reps=float(reps); observed+=1
                    samples.setdefault(name,[]).append(dict(sample=sample,variant=variant,Reps=reps,NS=float(ns)*reps,Bytes=float(size)*reps,Allocs=float(allocs)*reps))
                if observed!=(60 if case is None else 1): raise RuntimeError('missing benchmark observations')
        temporary=samples_path.with_suffix('.new')
        temporary.write_text(json.dumps(samples,indent=2)+'\n'); temporary.replace(samples_path)
        if sample in (19,59):
            results=report()
            active=[r['case'] for r in results if r['verdict']=='inconclusive' and r['samples']<60]
            if not active: break
    if any(r['verdict']!='pass' for r in results): raise SystemExit(1)

if __name__=='__main__': main()
