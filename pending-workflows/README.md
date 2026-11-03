These are the agent image's workflow files, which belong in
.github/workflows/. They are here only because the account that opened this
pull request may not write workflow files. Before merging, a maintainer runs:

    cp pending-workflows/*.yml .github/workflows/ && git rm -rq pending-workflows && git add -A .github/workflows && git commit -qm "workflows: build, scan, bump and publish the agent image" && git push
