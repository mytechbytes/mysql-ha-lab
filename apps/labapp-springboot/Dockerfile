# syntax=docker/dockerfile:1
FROM maven:3.9-eclipse-temurin-25 AS build
WORKDIR /src
COPY pom.xml .
RUN --mount=type=cache,target=/root/.m2 \
    mvn -B -q dependency:go-offline

COPY src ./src
RUN --mount=type=cache,target=/root/.m2 \
    mvn -B -q package -DskipTests \
 && cp target/*.jar /app.jar

# ---------------------------------------------------------------------------
# Stage 2: run on a small JRE 25 image, as a non-root user
# ---------------------------------------------------------------------------
FROM eclipse-temurin:25-jre

# unprivileged user for the app
RUN groupadd --system app && useradd --system --gid app --no-create-home app

WORKDIR /app
COPY --from=build /app.jar /app/app.jar
USER app

EXPOSE 8080

ENV JAVA_TOOL_OPTIONS="-XX:MaxRAMPercentage=75 -XX:+ExitOnOutOfMemoryError"

ENTRYPOINT ["java", "-jar", "/app/app.jar"]
