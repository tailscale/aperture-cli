# aperture-launcher Architecture

## Overview

`cmd/launcher` makes it very easy to launch a coding agent
preconfigured to work with Aperture.

Coding agents have different environment variables and configuration
files that they use to work with various upstream providers. Managing
these can be complex and error prone. launcher captures this complex
into Profiles.

A profile exists for each supported coding agant and the backends it
works with.

