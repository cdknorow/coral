# union_testcloud.py package.json: resolve one conflict on the "test:cloud" line by taking the union of both
# sides' commands (ours' order, each theirs-only command inserted after its predecessor in theirs).
import sys,re
p=sys.argv[1]; L=open(p).read().split('\n')
a=[i for i,l in enumerate(L) if l.startswith('<<<<<<< ')]; assert len(a)==1, a; a=a[0]
assert L[a+2]=='=======' and L[a+4].startswith('>>>>>>> '), "not a one-line conflict"
ours,theirs=L[a+1],L[a+3]
m=re.match(r'(\s*"test:cloud": ")(.*)(",?\s*)$', ours); assert m, "ours is not the test:cloud line"; pre,ov,post=m.groups()
tm=re.match(r'\s*"test:cloud": "(.*)",?\s*$', theirs); assert tm, "theirs is not the test:cloud line"
o=ov.split(' && '); t=tm.group(1).split(' && ')
added=[]
for i,x in enumerate(t):
    if x not in o:
        o.insert(o.index(t[i-1])+1 if i else 0, x); added.append(x)
dropped=[x for x in ov.split(' && ') if x not in t]
L[a:a+5]=[pre+' && '.join(o)+post]
open(p,'w').write('\n'.join(L))
print("kept ours-only:", dropped, "| added theirs-only:", added, "| total:", len(o))
