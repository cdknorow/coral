p='coral-go/internal/claudestub/claudestub.go'
s=open(p).read()
i=s.index('#!/bin/sh')
j=s.index('\n',i)+1
s=s[:j]+'sleep 1.7\n'+s[j:]
open(p,'w').write(s)
