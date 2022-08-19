const childProcess = require('child_process')
const os = require('os')
const process = require('process')

function main() {
    const mainScript = `${__dirname}/fhp`
    const spawnSyncReturns = childProcess.spawnSync(mainScript, { stdio: 'inherit' })
    const status = spawnSyncReturns.status
    if (typeof status === 'number') {
        process.exit(status)
    }
    process.exit(1)
}

if (require.main === module) {
    main()
}