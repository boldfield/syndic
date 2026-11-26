// forker is the helper of the macOS termination test: a process that forks
// detached children while termination runs. It is bounded in this source:
//
//   - at most MAX_PROCESSES processes in total, this one included: the
//     loop below is the only place that forks, it runs only in this first
//     process, and it forks at most MAX_PROCESSES - 1 times;
//   - every process arms alarm(LIFETIME) before anything else, so that each
//     exits within LIFETIME seconds whatever happens (a pending alarm is not
//     inherited across fork, so each child arms its own);
//   - a child never forks: it reports itself and pauses until it is killed
//     or its alarm ends it.
//
// SIGTERM is ignored throughout, so that the termination sequence reaches
// SIGKILL while this process is still forking.
#include <signal.h>
#include <stdio.h>
#include <unistd.h>

#define MAX_PROCESSES 150
#define LIFETIME 20

static void child(void) {
	alarm(LIFETIME);
	setsid();
	printf("child %d\n", getpid());
	fflush(stdout);
	for (;;)
		pause();
}

int main(void) {
	alarm(LIFETIME);
	signal(SIGTERM, SIG_IGN);
	printf("ready %d\n", getpid());
	fflush(stdout);
	for (int n = 1; n < MAX_PROCESSES; n++) {
		pid_t pid = fork();
		if (pid == 0) {
			child();
			_exit(0);
		}
		if (pid > 0) {
			printf("forked %d\n", n);
			fflush(stdout);
		}
		usleep(20000);
	}
	for (;;)
		pause();
}
