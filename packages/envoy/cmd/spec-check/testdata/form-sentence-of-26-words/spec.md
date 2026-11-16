:::requirement{#a-claim-survives-a-restart kind="behavior"}
Legion keeps the claim of each issue in Dispatch, and the daemon reads that claim again after each restart of the machine that runs the daemon.
:::

:::check{#a-lost-claim-is-reported serves="a-claim-survives-a-restart" subject="production" procedure="agent"}
Claim: The daemon reports a claim that it cannot read after a restart.
Procedure: An agent stops the database during a restart and reads the log of the daemon.
It must find one report for the claim.
Failing example: A daemon that starts with no claims and writes no report.
:::
