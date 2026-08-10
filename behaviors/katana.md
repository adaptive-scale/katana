# What this part of the product does

Katana is a command-line tool that generates test code from written product behavior and keeps that test code in sync as the behaviors change. This part is the program's entry point: it takes what the user typed on the command line, runs the requested command, and decides what the process reports back to the shell.

## Running a command

- Everything the user types after the program name is passed through as the command's arguments; the program name itself is not included.
- When the command completes without an error, the process exits successfully and nothing is written to the error stream.

## Reporting failures

- When the command fails, the process exits with status code 1.
- A failure message is written to the standard error stream, prefixed with `katana: ` followed by the error text and a newline.
- Failure messages go to the error stream only, never to the standard output stream.

## Asking for usage

- When the user asks for usage or help, the process still exits with status code 1.
- A help request produces no `katana: ` message, because the usage text has already been shown to the user.
